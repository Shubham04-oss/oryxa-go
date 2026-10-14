// The call's media: RealtimeKit's core SDK behind a small interface, so the
// UI never touches the SDK and tests can stand in for it. The SDK is loaded
// only when a member joins.
export type CallTile = {
  /** RealtimeKit's peer id. */
  id: string
  /** The member's user id (RealtimeKit's custom participant id). */
  member: string
  name: string
  self: boolean
  audio: boolean
  video: boolean
  videoTrack?: MediaStreamTrack
  audioTrack?: MediaStreamTrack
  screenTrack?: MediaStreamTrack
}

export type HeardSegment = {
  id: string
  member: string
  name: string
  text: string
  partial: boolean
  at: number
}

export type DeviceKind = "audioinput" | "videoinput" | "audiooutput"

/** The devices the member can choose between, and the ones in use. */
export type CallDevices = {
  microphones: MediaDeviceInfo[]
  cameras: MediaDeviceInfo[]
  speakers: MediaDeviceInfo[]
  current: Partial<Record<DeviceKind, string>>
}

export interface CallMedia {
  tiles: () => CallTile[]
  /** Called whenever anyone's tiles change; returns the unsubscribe. */
  onChange: (listener: () => void) => () => void
  /** Every speaker's live transcript, as RealtimeKit hears it. */
  onTranscript: (listener: (segment: HeardSegment) => void) => () => void
  devices: () => Promise<CallDevices>
  /** Switches to the device with `deviceId`, of its kind. */
  setDevice: (deviceId: string) => Promise<void>
  /** Called when a device is plugged in or out; returns the unsubscribe. */
  onDevicesChange: (listener: () => void) => () => void
  setMic: (on: boolean) => Promise<void>
  setCamera: (on: boolean) => Promise<void>
  setScreen: (on: boolean) => Promise<void>
  leave: () => Promise<void>
}

export type MediaOptions = { token: string; mic: boolean; camera: boolean }

type Participant = {
  id: string
  name: string
  customParticipantId?: string
  audioEnabled: boolean
  videoEnabled: boolean
  screenShareEnabled: boolean
  audioTrack?: MediaStreamTrack
  videoTrack?: MediaStreamTrack
  screenShareTracks?: { video?: MediaStreamTrack }
  on?: (event: string, listener: () => void) => void
  off?: (event: string, listener: () => void) => void
}

function tileOf(participant: Participant, self: boolean): CallTile {
  return {
    id: participant.id,
    member: participant.customParticipantId ?? participant.id,
    name: participant.name,
    self,
    audio: participant.audioEnabled,
    video: participant.videoEnabled,
    audioTrack: participant.audioEnabled ? participant.audioTrack : undefined,
    videoTrack: participant.videoEnabled ? participant.videoTrack : undefined,
    screenTrack: participant.screenShareEnabled
      ? participant.screenShareTracks?.video
      : undefined,
  }
}

/** Joins the RealtimeKit meeting the edge issued `token` for. */
export async function joinMedia(options: MediaOptions): Promise<CallMedia> {
  const { default: RealtimeKitClient } = await import("@cloudflare/realtimekit")
  const meeting = await RealtimeKitClient.init({
    authToken: options.token,
    defaults: { audio: options.mic, video: options.camera },
  })
  await meeting.join()

  const listeners = new Set<() => void>()
  const changed = () => {
    for (const listener of listeners) listener()
  }
  const mediaEvents = ["audioUpdate", "videoUpdate", "screenShareUpdate"]
  const self = meeting.self as unknown as Participant
  for (const event of mediaEvents) self.on?.(event, changed)
  // The map passes on each participant's own events, so one listener each
  // covers everyone in the call.
  const joined = meeting.participants.joined as unknown as Map<
    string,
    Participant
  > & { on: (event: string, listener: () => void) => void }
  for (const event of [
    ...mediaEvents,
    "participantJoined",
    "participantLeft",
  ]) {
    joined.on(event, changed)
  }

  return {
    tiles: () => [
      tileOf(self, true),
      ...[...joined.values()].map((participant) => tileOf(participant, false)),
    ],
    onChange(listener) {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    onTranscript(listener) {
      const heard = (transcript: {
        id: string
        name: string
        peerId: string
        customParticipantId: string
        transcript: string
        isPartialTranscript: boolean
        date: Date
      }) =>
        listener({
          id: transcript.id,
          member: transcript.customParticipantId || transcript.peerId,
          name: transcript.name,
          text: transcript.transcript,
          partial: transcript.isPartialTranscript,
          at: new Date(transcript.date).getTime(),
        })
      meeting.ai.on("transcript", heard)
      return () => meeting.ai.off("transcript", heard)
    },
    async devices() {
      const media = meeting.self as unknown as {
        getAudioDevices: () => Promise<MediaDeviceInfo[]>
        getVideoDevices: () => Promise<MediaDeviceInfo[]>
        getSpeakerDevices: () => Promise<MediaDeviceInfo[]>
        getCurrentDevices: () => Partial<{
          audio: MediaDeviceInfo
          video: MediaDeviceInfo
          speaker: MediaDeviceInfo
        }>
      }
      const [microphones, cameras, speakers] = await Promise.all([
        media.getAudioDevices(),
        media.getVideoDevices(),
        media.getSpeakerDevices().catch(() => []),
      ])
      const current = media.getCurrentDevices()
      return {
        microphones,
        cameras,
        speakers,
        current: {
          audioinput: current.audio?.deviceId,
          videoinput: current.video?.deviceId,
          audiooutput: current.speaker?.deviceId,
        },
      }
    },
    async setDevice(deviceId) {
      const all = await (
        meeting.self as unknown as {
          getAllDevices: () => Promise<MediaDeviceInfo[]>
        }
      ).getAllDevices()
      const device = all.find((item) => item.deviceId === deviceId)
      if (device) await meeting.self.setDevice(device)
      changed()
    },
    onDevicesChange(listener) {
      const self = meeting.self as unknown as {
        on: (event: string, listener: () => void) => void
        off: (event: string, listener: () => void) => void
      }
      self.on("deviceListUpdate", listener)
      self.on("deviceUpdate", listener)
      return () => {
        self.off("deviceListUpdate", listener)
        self.off("deviceUpdate", listener)
      }
    },
    async setMic(on) {
      if (on) await meeting.self.enableAudio()
      else await meeting.self.disableAudio()
      changed()
    },
    async setCamera(on) {
      if (on) await meeting.self.enableVideo()
      else await meeting.self.disableVideo()
      changed()
    },
    async setScreen(on) {
      if (on) await meeting.self.enableScreenShare()
      else await meeting.self.disableScreenShare()
      changed()
    },
    async leave() {
      listeners.clear()
      await meeting.leave()
    },
  }
}
