#!/usr/bin/env bash
# Runs put-secrets.sh against stand-ins for tofu and the aws CLI, for each
# situation its Slack part has to handle. Nothing real is called: the script and
# lib.sh are copied into a repository of their own in a temporary directory, so
# no .env of yours is read, and the stand-ins answer only what the script asks
# of them and stop with an error on anything else. Everything is removed at the
# end.
#
#   deploy/scripts/put-secrets.test.sh
#
# The hidden prompts need a terminal, so they are not run here: on one, they read
# three values without echoing them, and hand them to the same checks.

set -u

SCRIPTS=$(cd "$(dirname "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/bin" "$WORK/store" "$WORK/repo/deploy/scripts"
cp "$SCRIPTS/put-secrets.sh" "$SCRIPTS/lib.sh" "$WORK/repo/deploy/scripts/"
git -C "$WORK/repo" init -q
SCRIPT="$WORK/repo/deploy/scripts/put-secrets.sh"

# tofu answers the two outputs lib.sh reads and does nothing else.
cat >"$WORK/bin/tofu" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  *"output -json provisioner_environment"*) echo '{"AWS_REGION":"us-west-2","QUEUE_NAMES":"{}"}' ;;
  *"output -raw name"*) printf 'oryxa-test' ;;
  *) echo "stand-in tofu: unexpected: $*" >&2; exit 99 ;;
esac
EOF

# aws keeps what put-secret-value is given on stdin in a file for each secret,
# says which containers exist, and logs every argument list it was called with.
cat >"$WORK/bin/aws" <<'EOF'
#!/usr/bin/env bash
store="$STUB_STORE"
printf '%s\n' "$*" >>"$store/argv.log"
case "$1 $2" in
  "secretsmanager put-secret-value" | "secretsmanager describe-secret") ;;
  *) echo "stand-in aws: unexpected: $*" >&2; exit 99 ;;
esac
subcommand=$2
shift 2
id="" query=""
while (($#)); do
  case "$1" in
    --secret-id) id=$2; shift 2 ;;
    --secret-string) [[ $2 == file:///dev/stdin ]] || { echo "stand-in aws: a value not on stdin" >&2; exit 98; }; shift 2 ;;
    --query) query=$2; shift 2 ;;
    *) shift ;;
  esac
done
case " $STUB_CONTAINERS " in
  *" $id "*) ;;
  *) echo "An error occurred (ResourceNotFoundException): Secrets Manager can't find the specified secret." >&2; exit 254 ;;
esac
file="$store/$(tr '/' '_' <<<"$id")"
case "$subcommand" in
  put-secret-value) cat >"$file" ;;
  describe-secret)
    if [[ -z $query ]]; then echo '{}'
    elif [[ -e $file ]]; then echo '{"0d2fd1d0-0000-0000-0000-000000000000": ["AWSCURRENT"]}'
    else echo null; fi ;;
esac
EOF
chmod +x "$WORK/bin/tofu" "$WORK/bin/aws"

# Never the real ones.
if [[ $(PATH="$WORK/bin:$PATH" command -v aws) != "$WORK/bin/aws" || $(PATH="$WORK/bin:$PATH" command -v tofu) != "$WORK/bin/tofu" ]]; then
  echo "the stand-ins are not what would run: refusing to go on" >&2
  exit 1
fi

BOTH="oryxa-test/slack-app oryxa-test/slack-ticket-key"
OTHERS="oryxa-test/composio-api-key oryxa-test/typesafe-api-key oryxa-test/realtime-purge-secret oryxa-test/apps-default-oauth-clients oryxa-test/apps-hook-secrets"
failures=0
out="" code=0

fresh() { rm -rf "$WORK/store" && mkdir -p "$WORK/store"; }

# run <containers that exist> [VAR=value ...]: the script's output goes to $out
# and its exit status to $code.
run() {
  local containers=$1
  shift
  out=$(env -i HOME="$HOME" PATH="$WORK/bin:$PATH" STUB_STORE="$WORK/store" STUB_CONTAINERS="$containers $OTHERS" \
    COMPOSIO_API_KEY=composio-key-for-the-test "$@" /bin/bash "$SCRIPT" </dev/null 2>&1)
  code=$?
}

check() { # check <what> <exit status of the condition>
  if [[ $2 -eq 0 ]]; then
    echo "ok    $1"
  else
    failures=$((failures + 1))
    echo "FAIL  $1"
    echo "----- output:"
    echo "$out" | sed 's/^/      /'
    echo "-----"
  fi
}

stored() { cat "$WORK/store/oryxa-test_$1" 2>/dev/null; }
has() { [[ -e $WORK/store/oryxa-test_$1 ]]; }

CLIENT_ID=1234567890.0987654321
CLIENT_SECRET=TESTCLIENTSECRET-abcdefghijklmnop
SIGNING_SECRET=TESTSIGNINGSECRET-0123456789abcdef
GOOD='{"client_id":"'$CLIENT_ID'","client_secret":"'$CLIENT_SECRET'","signing_secret":"'$SIGNING_SECRET'"}'

# 1. No containers, nothing to store: skipped, and everything else went on.
fresh
run ""
[[ $code -eq 0 && $out == *"Slack skipped"* ]]
check "no containers, no Slack values: skipped, exit 0" $?
has composio-api-key
check "the other secrets were still stored" $?
! has slack-app && ! has slack-ticket-key
check "nothing for Slack was stored" $?

# 2. No containers but a value given: refused, and it says why.
fresh
run "" APPS_SLACK_APP="$GOOD"
[[ $code -eq 1 && $out == *"does not exist yet"* ]]
check "no containers, a value given: refused" $?
[[ $out != *"$CLIENT_SECRET"* ]]
check "the refusal does not quote the secret" $?

# 3. Containers, nothing given, no terminal: the ticket key is made, no prompt.
fresh
run "$BOTH"
[[ $code -eq 0 && $out == *"made and stored AGENTS_PRIMARY_SLACK_TICKET_KEY"* ]]
check "the ticket key is made when there is none" $?
key=$(stored slack-ticket-key)
[[ $key =~ ^[0-9a-f]{64}$ && $(wc -c <"$WORK/store/oryxa-test_slack-ticket-key" | tr -d ' ') == 64 ]]
check "it is 64 hex characters with no newline" $?
! has slack-app
check "no app is stored without values" $?
first_key=$key

# 4. Again: kept.
run "$BOTH"
[[ $code -eq 0 && $out == *"stored already: left as it is"* && $(stored slack-ticket-key) == "$first_key" ]]
check "a second run keeps the ticket key" $?

# 5. A valid app, with padding, extra keys and .env's quotes.
fresh
run "$BOTH" APPS_SLACK_APP="'{\"client_id\":\"  $CLIENT_ID \",\"client_secret\":\" $CLIENT_SECRET\",\"signing_secret\":\"$SIGNING_SECRET \",\"extra\":\"dropped\"}'"
[[ $code -eq 0 && $out == *"stored APPS_SLACK_APP"* ]]
check "a valid app is stored" $?
[[ $(stored slack-app) == "$GOOD" ]]
check "it is stored as exactly the three trimmed values, compact" $?
[[ $out != *"$CLIENT_SECRET"* && $out != *"$SIGNING_SECRET"* ]]
check "the output does not quote them" $?

# 6. Mistakes are refused whole, and never quoted.
bad() { # bad <what> <APPS_SLACK_APP>
  fresh
  run "$BOTH" APPS_SLACK_APP="$2"
  [[ $code -eq 1 && $out == *"nothing for Slack was stored"* ]] && ! has slack-app && ! has slack-ticket-key
  check "refused: $1" $?
  [[ $out != *"$CLIENT_SECRET"* && $out != *"$SIGNING_SECRET"* && $out != *"1234567890"* ]]
  check "  and the refusal does not quote it: $1" $?
}
bad "a client secret of 15 characters" '{"client_id":"'$CLIENT_ID'","client_secret":"123456789012345","signing_secret":"'$SIGNING_SECRET'"}'
bad "no signing secret" '{"client_id":"'$CLIENT_ID'","client_secret":"'$CLIENT_SECRET'"}'
bad "not JSON" 'client_id='$CLIENT_ID
bad "an array" '["'$CLIENT_ID'","'$CLIENT_SECRET'","'$SIGNING_SECRET'"]'
bad "a client ID with a space in it" '{"client_id":"1234 567890","client_secret":"'$CLIENT_SECRET'","signing_secret":"'$SIGNING_SECRET'"}'
bad "a secret with a space in it" '{"client_id":"'$CLIENT_ID'","client_secret":"TESTCLIENTSECRET abcdefghijklmnop","signing_secret":"'$SIGNING_SECRET'"}'
bad "a secret that is not a string" '{"client_id":"'$CLIENT_ID'","client_secret":12345678901234567890,"signing_secret":"'$SIGNING_SECRET'"}'
bad "an empty client ID" '{"client_id":"  ","client_secret":"'$CLIENT_SECRET'","signing_secret":"'$SIGNING_SECRET'"}'
bad "a secret with a non-ASCII character" '{"client_id":"'$CLIENT_ID'","client_secret":"TESTCLIENTSECRET-abcdefghijklmnopé","signing_secret":"'$SIGNING_SECRET'"}'

# 7. The ticket key, given.
fresh
given=$(printf 'ab%.0s' {1..32})
run "$BOTH" AGENTS_PRIMARY_SLACK_TICKET_KEY="$given"
[[ $code -eq 0 && $(stored slack-ticket-key) == "$given" ]]
check "a ticket key that is given is stored as it is" $?
for wrong in "${given:0:63}" "${given}0" "'$given'" "${given:0:62}zz"; do
  fresh
  run "$BOTH" AGENTS_PRIMARY_SLACK_TICKET_KEY="$wrong" APPS_SLACK_APP="$GOOD"
  [[ $code -eq 1 && $out == *"exactly 64 hex characters"* ]] && ! has slack-ticket-key && ! has slack-app
  check "refused before anything is stored: a ticket key of ${#wrong} characters" $?
done

# 8. No argument list of anything called held a secret, and the app went on stdin.
fresh
run "$BOTH" APPS_SLACK_APP="$GOOD" AGENTS_PRIMARY_SLACK_TICKET_KEY="$given"
! grep -q -e "$CLIENT_SECRET" -e "$SIGNING_SECRET" -e "$given" -e "$CLIENT_ID" "$WORK/store/argv.log"
check "no aws argument list held a secret" $?
grep -q "secretsmanager put-secret-value --secret-id oryxa-test/slack-app --secret-string file:///dev/stdin" "$WORK/store/argv.log"
check "the app went to Secrets Manager on stdin" $?

echo
if ((failures)); then
  echo "$failures FAILED"
  exit 1
fi
echo "all passed"
