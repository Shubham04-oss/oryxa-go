-- A seat's turn in a room answers from the room alone, on an agent that
-- reaches none of its member's apps, knowledge, memory or requests
-- (managedagents.RoomSeat). The thread each seat kept for a room before that
-- holds turns its member's full agent took there, with what its tools read of
-- theirs, and a turn that went on in it would read all of that again. So the
-- room forgets every thread its seats kept, and each seat takes a new one on
-- its next turn (AgentSpeaker.Speak). The threads themselves stay with their
-- member, untouched. Safe to repeat: run again, it only has every seat take
-- a new thread once more.

DELETE FROM agents_primary.room_agent_threads;
