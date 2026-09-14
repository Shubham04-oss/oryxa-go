import { check, fail, sleep } from 'k6';
import http from 'k6/http';
import crypto from 'k6/crypto';
import encoding from 'k6/encoding';
import { Trend } from 'k6/metrics';

const org = __ENV.ORG_ID;
const teamsURL = (__ENV.TEAMS_URL || 'http://127.0.0.1:18083').replace(/\/$/, '');
const integrationsURL = (__ENV.INTEGRATIONS_URL || 'http://127.0.0.1:18084').replace(/\/$/, '');
const filesURL = (__ENV.FILES_URL || 'http://127.0.0.1:18085').replace(/\/$/, '');
const authURL = (__ENV.AUTH_URL || 'http://127.0.0.1:18080').replace(/\/$/, '');
const mailpitURL = (__ENV.MAILPIT_URL || 'http://127.0.0.1:8025').replace(/\/$/, '');
const runID = __ENV.RUN_ID || `${Date.now()}`;
const returnURI = 'https://app.example.test/settings/integrations';
const slackScopes = [
  'channels:history',
  'channels:read',
  'chat:write',
  'groups:history',
  'groups:read',
  'reactions:write',
];
const secrets = ['client-secret', 'rotated-secret', 'access-token', 'signing-secret', 'must-not-leak'];

const teamsDuration = new Trend('teams_req_duration');
const integrationsDuration = new Trend('integrations_req_duration');
const filesDuration = new Trend('files_req_duration');

export const options = {
  scenarios: {
    stack: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      maxDuration: '3m',
    },
  },
  thresholds: {
    checks: ['rate==1'],
    teams_req_duration: ['p(95)<2500'],
    integrations_req_duration: ['p(95)<4000'],
    files_req_duration: ['p(95)<2500'],
  },
};

export default function journey() {
  if (!org) {
    fail('ORG_ID is required');
  }

  health();
  clearMail();
  concealment();
  const teams = teamsJourney();
  filesJourney(teams);
  integrationsJourney();
  crossStack(teams.engineeringName);
  cleanup(teams);
}

function health() {
  for (const [name, url, want] of [
    ['auth', `${authURL}/healthz`, 204],
    ['teams', `${teamsURL}/healthz`, 204],
    ['integrations', `${integrationsURL}/healthz`, 204],
    ['files', `${filesURL}/healthz`, 204],
    ['mailpit', `${mailpitURL}/api/v1/info`, 200],
  ]) {
    const res = http.get(url);
    must(res, { [`${name} is up`]: (r) => r.status === want }, `${name} is not up`);
  }
}

function concealment() {
  must(t('GET', '/teams', '', null), { 'teams without credentials is 401': (r) => r.status === 401 });
  must(f('GET', '/files', '', null), { 'files without credentials is 401': (r) => r.status === 401 });
  must(i('GET', '/integrations', '', null), { 'integrations without credentials is 401': (r) => r.status === 401 });
  must(t('GET', '/teams', 'nope', null), { 'teams unknown token is 401': (r) => r.status === 401 });
  must(f('GET', '/files', 'nope', null), { 'files unknown token is 401': (r) => r.status === 401 });
  must(i('GET', '/integrations', 'nope', null), { 'integrations unknown token is 401': (r) => r.status === 401 });
  must(t('GET', '/teams', 'stranger', null), { 'teams conceals the org from a stranger': (r) => r.status === 404 });
  must(f('GET', '/files', 'stranger', null), { 'files conceals the org from a stranger': (r) => r.status === 404 });
  must(i('GET', '/integrations', 'stranger', null), { 'integrations forbids a stranger': (r) => r.status === 403 });
  must(i('GET', `/integrations`, 'owner', null, `${integrationsURL}/v1/organizations/not-${org}/integrations`), {
    'integrations conceals a foreign org': (r) => r.status === 403,
  });
}

function filesJourney(teams) {
  const notes = encoding.b64encode(`sim-notes ${runID}`);
  const personal = json(must(f('POST', '/files', 'owner', {
    filename: `notes-${runID}.txt`,
    content_type: 'text/plain',
    purpose: 'upload',
    content: notes,
  }), { 'owner stores a personal object': (r) => r.status === 201 }));
  mustOk(personal.id && personal.sha256 && personal.size_bytes > 0 && personal.purpose === 'upload', 'created file metadata is complete');
  mustOk(personal.organization_id === org && personal.scope === 'member' && personal.member_id, 'personal file hangs on the owner');
  must(f('GET', `/files/${personal.id}`, 'member', null), { 'another member cannot fetch a personal file': (r) => r.status === 404 });
  must(f('GET', `/files/${personal.id}/content`, 'outsider', null), { 'outsider cannot download a personal file': (r) => r.status === 404 });

  const memberList = json(must(f('GET', '/files', 'member', null), { 'member lists visible files': (r) => r.status === 200 }));
  mustOk(Array.isArray(memberList.files) && !memberList.files.some((row) => row.id === personal.id), 'member list does not include the owner personal object');

  const ownerList = json(must(f('GET', '/files', 'owner', null), { 'owner lists member and org files': (r) => r.status === 200 }));
  mustOk(ownerList.files.some((row) => row.id === personal.id), 'owner list includes the personal object');

  const createdAt = new Date(personal.created_at);
  const year = createdAt.getUTCFullYear();
  const month = createdAt.getUTCMonth() + 1;
  const day = createdAt.getUTCDate();
  const dayList = json(must(f('GET', `/files?scope=member&year=${year}&month=${month}&day=${day}`, 'owner', null), { 'owner lists files for that UTC day': (r) => r.status === 200 }));
  mustOk(dayList.files.some((row) => row.id === personal.id), 'day list includes the personal object');
  const otherYear = json(must(f('GET', `/files?scope=member&year=${year - 1}`, 'owner', null), { 'owner lists files for another year': (r) => r.status === 200 }));
  mustOk(!otherYear.files.some((row) => row.id === personal.id), "another year does not include today's file");

  const signed = json(must(f('POST', `/files/${personal.id}/download-url`, 'owner', {}), { 'owner gets a download url': (r) => r.status === 200 }));
  mustOk(signed.url && signed.expires_at, 'presigned download url is present');
  const fromS3 = http.get(signed.url);
  must(fromS3, { 'presigned S3 get returns the object': (r) => r.status === 200 && String(r.body) === `sim-notes ${runID}` });

  must(f('DELETE', `/files/${personal.id}`, 'member', null), { 'another member cannot delete the personal object': (r) => r.status === 404 });
  must(f('DELETE', `/files/${personal.id}`, 'owner', null), { 'owner deletes the personal object': (r) => r.status === 204 });
  must(f('GET', `/files/${personal.id}`, 'owner', null), { 'deleted personal object is gone': (r) => r.status === 404 });

  must(f('POST', '/files', 'member', {
    filename: `org-${runID}.txt`,
    content_type: 'text/plain',
    scope: 'organization',
    content: encoding.b64encode('nope'),
  }), { 'a member cannot create an organization file': (r) => r.status === 403 });

  const orgFile = json(must(f('POST', '/files', 'owner', {
    filename: `handbook-${runID}.txt`,
    content_type: 'text/plain',
    scope: 'organization',
    purpose: 'upload',
    content: encoding.b64encode(`handbook ${runID}`),
  }), { 'owner stores an organization file': (r) => r.status === 201 }));
  mustOk(orgFile.scope === 'organization' && !orgFile.team_id && !orgFile.member_id, 'organization file hangs on the org');
  const fetchedOrg = json(must(f('GET', `/files/${orgFile.id}`, 'member', null), { 'a member can fetch an organization file': (r) => r.status === 200 }));
  mustOk(fetchedOrg.id === orgFile.id && fetchedOrg.sha256 === orgFile.sha256, 'member sees the organization object');
  const orgList = json(must(f('GET', '/files?scope=organization', 'member', null), { 'member lists organization files': (r) => r.status === 200 }));
  mustOk(orgList.files.some((row) => row.id === orgFile.id), 'member org list includes the handbook');

  const invoice = json(must(f('POST', '/files', 'owner', {
    filename: `invoice-${runID}.pdf`,
    content_type: 'application/pdf',
    scope: 'organization',
    purpose: 'billing.attachment',
    content: encoding.b64encode(`invoice ${runID}`),
  }), { 'owner stores a billing attachment': (r) => r.status === 201 }));
  must(f('GET', `/files/${invoice.id}`, 'member', null), { 'billing attachments stay off members': (r) => r.status === 404 });
  must(f('GET', `/files/${invoice.id}`, 'admin', null), { 'org admin can fetch a billing attachment': (r) => r.status === 200 });
  const memberOrgList = json(must(f('GET', '/files?scope=organization', 'member', null), { 'member org list hides billing': (r) => r.status === 200 }));
  mustOk(!memberOrgList.files.some((row) => row.id === invoice.id), 'billing stays off the member org list');

  must(f('POST', '/files', 'outsider', {
    filename: `leak-${runID}.txt`,
    content_type: 'text/plain',
    scope: 'team',
    team_id: teams.engineering.id,
    content: encoding.b64encode('nope'),
  }), { 'outsider cannot hang a file on Engineering': (r) => r.status === 404 });

  const teamFile = json(must(f('POST', '/files', 'owner', {
    filename: `spec-${runID}.md`,
    content_type: 'text/markdown',
    scope: 'team',
    team_id: teams.engineering.id,
    purpose: 'upload',
    content: encoding.b64encode(`spec ${runID}`),
  }), { 'owner stores a team file on Engineering': (r) => r.status === 201 }));
  mustOk(teamFile.scope === 'team' && teamFile.team_id === teams.engineering.id, 'team file hangs on Engineering');
  must(f('GET', `/files/${teamFile.id}`, 'member', null), { 'Engineering member can fetch the team file': (r) => r.status === 200 });
  must(f('GET', `/files/${teamFile.id}`, 'outsider', null), { 'outsider cannot fetch the team file': (r) => r.status === 404 });
  const teamList = json(must(f('GET', `/files?team_id=${teams.engineering.id}`, 'member', null), { 'member lists Engineering files': (r) => r.status === 200 }));
  mustOk(teamList.files.some((row) => row.id === teamFile.id), 'Engineering list includes the spec');
  must(f('GET', `/files?team_id=${teams.engineering.id}`, 'outsider', null), { 'outsider Engineering file list stays 404': (r) => r.status === 404 });
  must(f('GET', `/files?team_id=${teams.design.id}`, 'member', null), { 'member cannot list Design files': (r) => r.status === 404 });

  const attachment = json(must(f('POST', '/files', 'member', {
    filename: `slack-${runID}.txt`,
    content_type: 'text/plain',
    scope: 'team',
    team_id: teams.engineering.id,
    purpose: 'integrations.attachment',
    content: encoding.b64encode('slack note'),
  }), { 'member stores an integrations attachment on Engineering': (r) => r.status === 201 }));
  mustOk(attachment.purpose === 'integrations.attachment' && attachment.team_id === teams.engineering.id, 'integrations attachment hangs on Engineering');

  must(f('DELETE', `/files/${orgFile.id}`, 'member', null), { 'member cannot delete an organization file': (r) => r.status === 404 });
  must(f('DELETE', `/files/${orgFile.id}`, 'owner', null), { 'owner deletes the organization file': (r) => r.status === 204 });
  must(f('DELETE', `/files/${invoice.id}`, 'owner', null), { 'owner deletes the billing attachment': (r) => r.status === 204 });
  must(f('DELETE', `/files/${teamFile.id}`, 'outsider', null), { 'outsider cannot delete the team file': (r) => r.status === 404 });
  must(f('DELETE', `/files/${teamFile.id}`, 'owner', null), { 'owner deletes the team file': (r) => r.status === 204 });
  must(f('DELETE', `/files/${attachment.id}`, 'member', null), { 'creator deletes their team attachment': (r) => r.status === 204 });
}

function teamsJourney() {
  const engineeringName = `Sim Engineering ${runID}`;
  const designName = `Sim Design ${runID}`;
  const platformName = `Sim Platform ${runID}`;

  const catalog = json(must(t('GET', '/tools', 'owner', null), { 'owner sees the tool catalog': (r) => r.status === 200 }));
  mustOk(catalog.tools && catalog.tools.length === 3, 'catalog must be the three tools');
  mustOk(catalog.tools[0].name === 'list_teams' && catalog.tools[1].name === 'call_team' && catalog.tools[2].name === 'call_team_members', 'catalog order is list, call, members');

  must(t('POST', '/teams', 'member', { name: engineeringName }), { 'a team member cannot create a team': (r) => r.status === 403 });
  must(t('POST', '/teams', 'outsider', { name: engineeringName }), { 'an org member off every team cannot create': (r) => r.status === 403 });

  const engineering = json(must(t('POST', '/teams', 'owner', { name: engineeringName }), { 'owner creates Engineering': (r) => r.status === 201 }));
  const design = json(must(t('POST', '/teams', 'admin', { name: designName }), { 'org admin creates Design': (r) => r.status === 201 }));
  mustOk(engineering.id && engineering.path && engineering.name === engineeringName, 'created Engineering is complete');
  mustOk(design.id && design.organization_id === org, 'created Design belongs to the org');

  const ownerTeams = json(must(t('GET', '/teams', 'owner', null), { 'owner lists every team': (r) => r.status === 200 }));
  mustOk(ownerTeams.teams.length === 2 && ownerTeams.teams.every((row) => row.can_manage), 'owner manages both teams');

  must(t('POST', `/teams/${engineering.id}/members`, 'owner', { user_id: 'user-stranger', role: 'member' }), {
    'cannot add someone who is not in the org': (r) => r.status === 400,
  });
  const added = json(must(t('POST', `/teams/${engineering.id}/members`, 'owner', { user_id: 'user-member', role: 'member' }), {
    'owner adds the member to Engineering': (r) => r.status === 201,
  }));
  mustOk(added.user_id === 'user-member' && added.role === 'member' && added.notify_email === true, 'new member starts with mail on');
  mustOk(added.path && added.path.startsWith(`${engineering.path}.`), 'new member hangs under the team');

  const memberTeams = json(must(t('GET', '/teams', 'member', null), { 'member lists only Engineering': (r) => r.status === 200 }));
  mustOk(memberTeams.teams.length === 1 && memberTeams.teams[0].id === engineering.id && memberTeams.teams[0].can_manage === false, 'member sees one team and cannot manage it');

  const cookieTeams = json(must(cookieGet(`${teamsURL}/v1/organizations/${org}/teams`, 'member'), { 'member cookie lists the same team': (r) => r.status === 200 }));
  mustOk(cookieTeams.teams.length === 1 && cookieTeams.teams[0].id === engineering.id, 'cookie session matches bearer');

  const outsiderTeams = json(must(t('GET', '/teams', 'outsider', null), { 'outsider lists no teams': (r) => r.status === 200 }));
  mustOk(outsiderTeams.teams.length === 0, 'outsider is in the org but on no team');

  const frontendName = `Sim Frontend ${runID}`;
  must(t('POST', '/teams', 'member', { name: frontendName, parent_id: engineering.id }), { 'a team member cannot nest a team': (r) => r.status === 403 });
  const frontend = json(must(t('POST', '/teams', 'owner', { name: frontendName, parent_id: engineering.id }), { 'owner nests Frontend under Engineering': (r) => r.status === 201 }));
  mustOk(frontend.path && frontend.path.startsWith(`${engineering.path}.`), 'child path hangs under the parent');
  must(t('GET', `/teams/${frontend.id}`, 'member', null), { 'member cannot see a child they are not on': (r) => r.status === 404 });
  const tree = json(must(t('GET', `/teams/${engineering.id}/descendants`, 'owner', null), { 'owner reads the Engineering tree': (r) => r.status === 200 }));
  mustOk(tree.teams.length === 2 && tree.teams.some((row) => row.id === frontend.id), 'descendants include the child');
  const memberTree = json(must(t('GET', `/teams/${engineering.id}/descendants`, 'member', null), { 'member descendants hide the child': (r) => r.status === 200 }));
  mustOk(memberTree.teams.length === 1 && memberTree.teams[0].id === engineering.id, 'member descendants stay on visible teams');
  must(t('GET', `/teams/${design.id}/descendants`, 'member', null), { 'hidden team descendants stay 404': (r) => r.status === 404 });
  const treePeople = json(must(t('GET', `/teams/${engineering.id}/members/descendants`, 'owner', null), { 'owner reads people under Engineering': (r) => r.status === 200 }));
  mustOk(
    treePeople.members.some((row) => row.user_id === 'user-member' && row.team_id === engineering.id) &&
      treePeople.members.some((row) => row.user_id === 'user-owner' && row.team_id === frontend.id),
    'member descendants include the nested team'
  );
  const memberPeople = json(must(t('GET', `/teams/${engineering.id}/members/descendants`, 'member', null), { 'member people descendants hide the child': (r) => r.status === 200 }));
  mustOk(
    memberPeople.members.some((row) => row.team_id === engineering.id) && !memberPeople.members.some((row) => row.team_id === frontend.id),
    'member people descendants stay on visible teams'
  );
  must(t('GET', `/teams/${design.id}/members/descendants`, 'member', null), { 'hidden team member descendants stay 404': (r) => r.status === 404 });

  const found = json(must(t('GET', `/teams/search?q=${encodeURIComponent(engineeringName)}`, 'owner', null), { 'owner search finds Engineering': (r) => r.status === 200 }));
  mustOk(found.teams.some((row) => row.id === engineering.id), 'search hit is Engineering');
  const memberSearch = json(must(t('GET', `/teams/search?q=${encodeURIComponent(designName)}`, 'member', null), { 'member search does not leak Design': (r) => r.status === 200 }));
  mustOk(!memberSearch.teams.some((row) => row.id === design.id), 'Design stays hidden from search');

  const embedding = Array(1536).fill(0);
  embedding[0] = 1;
  must(t('PUT', `/teams/${engineering.id}/embedding`, 'member', { embedding }), { 'a team member cannot write an embedding': (r) => r.status === 403 });
  must(t('PUT', `/teams/${engineering.id}/embedding`, 'owner', { embedding }), { 'owner writes the Engineering embedding': (r) => r.status === 204 });
  const nearest = json(must(t('POST', '/teams/nearest', 'owner', { embedding, limit: 5 }), { 'owner nearest-neighbour finds Engineering': (r) => r.status === 200 }));
  mustOk(nearest.teams[0] && nearest.teams[0].id === engineering.id, 'nearest team is Engineering');

  const people = json(must(t('GET', `/members/search?q=member`, 'owner', null), { 'owner fuzzy-searches members': (r) => r.status === 200 }));
  const memberHit = (people.members || []).find((row) => row.user_id === 'user-member');
  mustOk(memberHit && memberHit.path && memberHit.teams.some((team) => team.id === engineering.id && team.member_path), 'member search says they are on Engineering');
  const unplaced = json(must(t('GET', `/members/search?q=outsider`, 'owner', null), { 'owner finds an org member on no team': (r) => r.status === 200 }));
  const outsiderHit = (unplaced.members || []).find((row) => row.user_id === 'user-outsider');
  mustOk(outsiderHit && outsiderHit.path && outsiderHit.teams.length === 0, 'unplaced member has an empty teams list');
  const hiddenAdmin = json(must(t('GET', `/members/search?q=admin`, 'member', null), { 'member searches people without leaking Design': (r) => r.status === 200 }));
  const adminHit = (hiddenAdmin.members || []).find((row) => row.user_id === 'user-admin');
  mustOk(adminHit && !adminHit.teams.some((team) => team.id === design.id), 'Design stays off the member search');

  must(t('GET', `/teams/${design.id}`, 'member', null), { 'member cannot see Design': (r) => r.status === 404 });
  must(t('GET', `/teams/${engineering.id}`, 'outsider', null), { 'outsider cannot see Engineering': (r) => r.status === 404 });
  must(t('PATCH', `/teams/${design.id}`, 'member', { name: 'Nope' }), { 'hidden team stays 404 on mutate': (r) => r.status === 404 });
  must(t('PATCH', `/teams/${engineering.id}`, 'member', { name: 'Nope' }), { 'member cannot rename Engineering': (r) => r.status === 403 });
  must(t('PATCH', `/teams/${engineering.id}/notification`, 'outsider', { notify_email: false }), { 'outsider cannot flip Engineering mail': (r) => r.status === 404 });

  const adminDesign = json(must(t('GET', `/teams/${design.id}`, 'admin', null), { 'org admin can see Design': (r) => r.status === 200 }));
  mustOk(adminDesign.can_manage === true && adminDesign.team.id === design.id, 'org admin manages Design without being on it');

  const renamed = json(must(t('PATCH', `/teams/${engineering.id}`, 'owner', { name: platformName }), { 'owner renames Engineering': (r) => r.status === 200 }));
  mustOk(renamed.name === platformName, 'rename stuck');

  must(t('POST', '/tools/call_team', 'member', { query: engineeringName }), { 'old name no longer resolves': (r) => r.status === 404 });
  const called = json(must(t('POST', '/tools/call_team', 'member', { query: platformName }), { 'member calls the renamed team': (r) => r.status === 200 }));
  mustOk(called.id === engineering.id && called.name === platformName && called.path === engineering.path, 'call_team returns the handle');
  waitForMail('owner@sim.oryxa', platformName, 1, 'owner is mailed when someone else calls the team');
  must(t('POST', '/tools/call_team', 'member', { query: designName }), { 'member cannot call Design': (r) => r.status === 404 });
  const adminCall = json(must(t('POST', '/tools/call_team', 'admin', { query: designName }), { 'org admin can call Design': (r) => r.status === 200 }));
  mustOk(adminCall.id === design.id, 'admin call_team is Design');

  const listed = json(must(t('POST', '/tools/list_teams', 'member', {}), { 'member list_teams hides Design': (r) => r.status === 200 }));
  mustOk(Array.isArray(listed) && listed.length === 1 && listed[0].id === engineering.id, 'list_teams is a handle array');
  const ownerListed = json(must(t('POST', '/tools/list_teams', 'owner', {}), { 'owner list_teams sees both': (r) => r.status === 200 }));
  mustOk(Array.isArray(ownerListed) && ownerListed.length === 3, 'owner lists Engineering, Design, and Frontend');

  const roster = json(must(t('POST', '/tools/call_team_members', 'member', { query: platformName }), { 'member calls the roster': (r) => r.status === 200 }));
  mustOk(roster.team_id === engineering.id && roster.members.length === 2, 'roster has owner and member');
  const memberRow = roster.members.find((row) => row.user_id === 'user-member');
  mustOk(memberRow && memberRow.email === 'member@sim.oryxa' && memberRow.notify_email === true, 'roster carries email and mail switch');
  waitForMail('owner@sim.oryxa', platformName, 2, 'owner is mailed again for call_team_members');
  mustOk(mailCount('member@sim.oryxa') === 0, 'the caller is not mailed');

  const switched = json(must(t('PATCH', `/teams/${engineering.id}/notification`, 'member', { notify_email: false }), { 'member turns mail off': (r) => r.status === 200 }));
  mustOk(switched.notify_email === false && switched.user_id === 'user-member', 'mail switch is the caller only');
  must(t('POST', '/tools/call_team', 'owner', { query: platformName }), { 'owner calls the team after the switch': (r) => r.status === 200 });
  sleep(2);
  mustOk(mailCount('member@sim.oryxa') === 0, 'mail stays off after the member switch');
  const members = json(must(t('GET', `/teams/${engineering.id}/members`, 'owner', null), { 'owner reads members after the switch': (r) => r.status === 200 }));
  const after = members.members.find((row) => row.user_id === 'user-member');
  mustOk(after && after.notify_email === false, 'switch persisted');

  must(t('DELETE', `/teams/${design.id}/members/user-admin`, 'admin', null), { 'last Design admin cannot leave': (r) => r.status === 409 });

  const promoted = json(must(t('PATCH', `/teams/${engineering.id}/members/user-member`, 'owner', { role: 'admin' }), { 'owner promotes the member': (r) => r.status === 200 }));
  mustOk(promoted.role === 'admin', 'member is now team admin');
  const asAdmin = json(must(t('GET', `/teams/${engineering.id}`, 'member', null), { 'promoted member can manage': (r) => r.status === 200 }));
  mustOk(asAdmin.can_manage === true, 'team admin can_manage');

  return { engineering, design, engineeringName: platformName, designName };
}

function integrationsJourney() {
  must(i('POST', '/integration-apps', 'member', slackApp('client-secret')), { 'a member cannot register a Slack app': (r) => r.status === 403 });
  must(i('POST', '/integration-apps', 'outsider', slackApp('client-secret')), { 'an outsider cannot register a Slack app': (r) => r.status === 403 });
  must(i('POST', '/integration-apps', 'owner', { provider: 'slack', client_id: 'client-id', client_secret: 'client-secret', scopes: ['not-a-scope'] }), {
    'unknown Slack scopes are rejected': (r) => r.status === 400,
  });

  const created = must(i('POST', '/integration-apps', 'owner', slackApp('client-secret')), { 'owner registers Slack BYOC': (r) => r.status === 201 });
  assertNoSecrets(created, 'register Slack');
  const profile = json(created);
  mustOk(profile.id && profile.provider === 'slack' && profile.client_id === 'client-id', 'profile is safe metadata');

  const memberCatalog = json(must(i('GET', '/integrations', 'member', null), { 'member catalog after register': (r) => r.status === 200 }));
  mustOk(memberCatalog.items.some((item) => item.profile_id === profile.id && item.state === 'available'), 'member sees Slack as available');
  mustOk(!JSON.stringify(memberCatalog).includes('client-id'), 'member catalog omits the client id');
  assertNoSecrets(memberCatalog, 'member catalog');

  const begin = json(must(i('POST', '/connections/authorize', 'owner', { profile_id: profile.id, return_uri: returnURI }), { 'owner starts Slack OAuth': (r) => r.status === 201 }));
  mustOk(begin.authorization_url && begin.authorization_url.includes('state='), 'authorize URL carries state');
  const state = queryParam(begin.authorization_url, 'state');
  mustOk(state, 'OAuth state is present');

  const callback = http.get(`${integrationsURL}/v1/oauth/callback/slack?state=${encodeURIComponent(state)}&code=provider-code`, { redirects: 0 });
  must(callback, { 'Slack callback redirects home': (r) => r.status === 303 });
  const location = callback.headers.Location || callback.headers.location;
  mustOk(location && location.startsWith(returnURI), 'callback returns to the allowed URI');
  const connectionID = queryParam(location, 'connection_id');
  mustOk(connectionID, 'callback issued a connection id');

  const ownerCatalog = json(must(i('GET', '/integrations', 'owner', null), { 'owner catalog is connected': (r) => r.status === 200 }));
  const connected = ownerCatalog.items.find((item) => item.profile_id === profile.id);
  mustOk(connected && connected.state === 'connected' && connected.connection && connected.connection.id === connectionID, 'owner connection is listed');
  mustOk(connected.connection.identity && connected.connection.identity.account_id === 'T123', 'safe Slack identity is present');
  assertNoSecrets(ownerCatalog, 'owner catalog');

  const stillAvailable = json(must(i('GET', '/integrations', 'member', null), { 'other members stay available': (r) => r.status === 200 }));
  mustOk(stillAvailable.items.some((item) => item.profile_id === profile.id && item.state === 'available'), 'grants are member-owned');

  must(i('POST', '/triggers', 'member', { connection_id: connectionID, trigger_name: 'new-message', props: {} }), {
    'member cannot enable a trigger on someone else': (r) => r.status === 403,
  });

  const route = json(must(i('POST', `/integration-apps/${profile.id}/webhook-routes`, 'owner', { trigger_name: 'new-message', signing_secret: 'signing-secret' }), {
    'owner registers the Slack webhook route': (r) => r.status === 201,
  }));
  mustOk(route.opaque_route && route.webhook_url && route.webhook_url.endsWith(route.opaque_route), 'opaque webhook route');
  assertNoSecrets(route, 'webhook route');

  const trigger = json(must(i('POST', '/triggers', 'owner', { connection_id: connectionID, trigger_name: 'new-message', props: { ignoreBots: true } }), {
    'owner enables new-message': (r) => r.status === 201,
  }));
  mustOk(trigger.id && trigger.trigger_name === 'new-message', 'trigger id returned');

  const actionPath = `/v1/organizations/${org}/connections/${connectionID}/actions/post-message`;
  const actionBody = { props: { channel: 'C123', text: 'private action content' } };
  const action = must(i('POST', `/connections/${connectionID}/actions/post-message`, 'owner', actionBody, `${integrationsURL}${actionPath}`, { 'Idempotency-Key': 'stack-action-1' }), {
    'owner posts a Slack message': (r) => r.status === 200,
  });
  assertNoSecrets(action, 'action');
  mustOk(!action.body.includes('private action content'), 'action result does not echo the message');
  const posted = json(action);
  mustOk(posted.replayed === false && String(action.body).includes('171.2'), 'first action is not a replay');

  const replay = json(must(i('POST', `/connections/${connectionID}/actions/post-message`, 'owner', actionBody, `${integrationsURL}${actionPath}`, { 'Idempotency-Key': 'stack-action-1' }), {
    'same idempotency key replays': (r) => r.status === 200,
  }));
  mustOk(replay.replayed === true && replay.execution_id === posted.execution_id, 'replay keeps the execution');

  must(i('POST', `/connections/${connectionID}/actions/post-message`, 'owner', { props: { channel: 'C999', text: 'other' } }, `${integrationsURL}${actionPath}`, { 'Idempotency-Key': 'stack-action-1' }), {
    'same key with different input conflicts': (r) => r.status === 409,
  });
  must(i('POST', `/connections/${connectionID}/actions/not-an-action`, 'owner', { props: {} }), { 'unknown action is rejected': (r) => r.status === 404 || r.status === 422 || r.status === 400 });
  must(i('POST', `/connections/${connectionID}/actions/post-message`, 'member', actionBody, `${integrationsURL}${actionPath}`, { 'Idempotency-Key': 'stack-action-member' }), {
    'member cannot run the owner connection': (r) => r.status === 403,
  });

  const now = Math.floor(Date.now() / 1000);
  const payload = JSON.stringify({
    event_id: `Ev${runID}`,
    team_id: 'T123',
    event: { type: 'message', channel_type: 'channel', channel: 'C123', user: 'U123', text: 'hello', ts: '1.23' },
  });
  const bad = http.post(`${integrationsURL}/v1/webhooks/${route.opaque_route}`, payload, {
    headers: {
      'Content-Type': 'application/json',
      'x-slack-request-timestamp': String(now),
      'x-slack-signature': 'v0=deadbeef',
    },
  });
  must(bad, { 'bad Slack signature is 401': (r) => r.status === 401 });

  const good = http.post(`${integrationsURL}/v1/webhooks/${route.opaque_route}`, payload, {
    headers: {
      'Content-Type': 'application/json',
      'x-slack-request-timestamp': String(now),
      'x-slack-signature': slackSignature('signing-secret', now, payload),
    },
  });
  must(good, { 'signed Slack event is accepted': (r) => r.status === 202 });
  assertNoSecrets(good, 'webhook admit');

  const missing = http.post(`${integrationsURL}/v1/webhooks/not-a-route`, payload, { headers: { 'Content-Type': 'application/json' } });
  must(missing, { 'unknown webhook route is 404': (r) => r.status === 404 });

  const rotated = must(i('PATCH', `/integration-apps/${profile.id}`, 'owner', { client_id: 'client-id-2', client_secret: 'rotated-secret' }), {
    'owner rotates the Slack secret': (r) => r.status === 200,
  });
  assertNoSecrets(rotated, 'rotate');
  mustOk(json(rotated).client_id === 'client-id-2', 'rotated client id is visible; secret is not');

  must(i('DELETE', `/connections/${connectionID}`, 'member', null), { 'member cannot disconnect the owner': (r) => r.status === 403 });
  must(i('DELETE', `/connections/${connectionID}`, 'owner', null), { 'owner disconnects Slack': (r) => r.status === 204 });
  const after = json(must(i('GET', '/integrations', 'owner', null), { 'catalog after disconnect': (r) => r.status === 200 }));
  mustOk(after.items.some((item) => item.profile_id === profile.id && item.state === 'available'), 'disconnect returns the app to available');

  must(i('DELETE', `/integration-apps/${profile.id}`, 'member', null), { 'member cannot disable the app': (r) => r.status === 403 });
  must(i('DELETE', `/integration-apps/${profile.id}`, 'owner', null), { 'owner disables the Slack app': (r) => r.status === 204 });
  const gone = json(must(i('GET', '/integrations', 'owner', null), { 'disabled app leaves the catalog': (r) => r.status === 200 }));
  mustOk(!gone.items.some((item) => item.profile_id === profile.id), 'disabled Slack app is hidden');
}

function crossStack(teamName) {
  const listed = json(must(t('POST', '/tools/list_teams', 'owner', {}), { 'same org still lists teams after Slack': (r) => r.status === 200 }));
  mustOk(Array.isArray(listed) && listed.some((team) => team.name === teamName), 'teams and integrations share the org');
  const called = json(must(t('POST', '/tools/call_team', 'member', { query: teamName }), { 'AI still calls the team': (r) => r.status === 200 }));
  mustOk(called.name === teamName, 'call_team after Slack still works');
}

function cleanup(teams) {
  must(t('DELETE', `/teams/${teams.design.id}`, 'owner', null), { 'owner deletes Design': (r) => r.status === 204 });
  must(t('GET', `/teams/${teams.design.id}`, 'admin', null), { 'deleted Design is gone': (r) => r.status === 404 });
  must(t('DELETE', `/teams/${teams.engineering.id}`, 'owner', null), { 'owner deletes Engineering': (r) => r.status === 204 });
}

function clearMail() {
  const res = http.del(`${mailpitURL}/api/v1/messages`);
  must(res, { 'mailpit inbox cleared': (r) => r.status === 200 });
}

function mailCount(to) {
  const res = http.get(`${mailpitURL}/api/v1/search?query=${encodeURIComponent('to:' + to)}`);
  if (res.status !== 200) {
    return -1;
  }
  const body = res.json();
  return (body.messages || []).length;
}

function waitForMail(to, subjectPart, want, message) {
  let count = 0;
  let matched = 0;
  for (let attempt = 0; attempt < 20; attempt++) {
    const res = http.get(`${mailpitURL}/api/v1/search?query=${encodeURIComponent('to:' + to)}`);
    if (res.status === 200) {
      const messages = res.json().messages || [];
      count = messages.length;
      matched = messages.filter((row) => String(row.Subject || '').includes(subjectPart)).length;
      if (matched >= want) {
        mustOk(true, message);
        return;
      }
    }
    sleep(0.5);
  }
  fail(`${message}: saw ${matched}/${want} mails to ${to} about ${subjectPart} (inbox ${count})`);
}

function slackApp(secret) {
  return { provider: 'slack', client_id: 'client-id', client_secret: secret, scopes: slackScopes };
}

function t(method, path, token, body, url) {
  return request(method, url || `${teamsURL}/v1/organizations/${org}${path}`, token, body, teamsDuration);
}

function f(method, path, token, body, url) {
  return request(method, url || `${filesURL}/v1/organizations/${org}${path}`, token, body, filesDuration);
}

function i(method, path, token, body, url, extraHeaders) {
  return request(method, url || `${integrationsURL}/v1/organizations/${org}${path}`, token, body, integrationsDuration, extraHeaders);
}

function request(method, url, token, body, trend, extraHeaders) {
  const headers = { Accept: 'application/json' };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  if (body !== null && body !== undefined) {
    headers['Content-Type'] = 'application/json';
  }
  if (extraHeaders) {
    Object.assign(headers, extraHeaders);
  }
  const res = http.request(method, url, body === null || body === undefined ? null : JSON.stringify(body), { headers, redirects: 0 });
  if (trend) {
    trend.add(res.timings.duration);
  }
  return res;
}

function cookieGet(url, token) {
  const res = http.get(url, { headers: { Accept: 'application/json', Cookie: `oryxa_access=${token}` } });
  teamsDuration.add(res.timings.duration);
  return res;
}

function slackSignature(secret, timestamp, body) {
  return `v0=${crypto.hmac('sha256', secret, `v0:${timestamp}:${body}`, 'hex')}`;
}

function queryParam(raw, name) {
  const match = String(raw).match(new RegExp(`[?&]${name}=([^&]+)`));
  return match ? decodeURIComponent(match[1]) : '';
}

function json(res) {
  try {
    return res.json();
  } catch (err) {
    fail(`invalid JSON (${res.status}): ${res.body}`);
  }
}

function must(res, checks, message) {
  const ok = check(res, checks);
  if (!ok) {
    fail(message || `${res.request ? res.request.method : ''} ${res.url} -> ${res.status}: ${res.body}`);
  }
  return res;
}

function mustOk(condition, message) {
  if (!check(condition, { [message]: () => condition })) {
    fail(message);
  }
}

function assertNoSecrets(res, where) {
  const body = typeof res === 'string' ? res : res.body || JSON.stringify(res);
  for (const secret of secrets) {
    if (String(body).includes(secret)) {
      fail(`${where} leaked ${secret}`);
    }
  }
}
