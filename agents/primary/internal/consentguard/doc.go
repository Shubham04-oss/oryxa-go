// Package consentguard reads the answer an owner's agent gives a question asked
// in Slack, before the person who asked reads it there.
//
// The answer guard (package answerguard) reads any answer one member's agent
// gives another for personal data, by its shape and, when asked, by whether it
// says more than its question asked for. What it does not know is what the
// owner allowed. In the Slack consent loop the owner says yes to an AI summary,
// or to excerpts for an hour, and the answer that goes into a channel has to
// keep to it, and to stand on something. This package holds those rules. It is
// pure: it reads texts and calls a Grounder, and knows nothing of the ledger,
// Slack or the agent that wrote them.
//
// # What it decides
//
// Check is given an answer's texts, the question, how far the owner let it go
// (Level), and what the run's tools returned (Source). Its Result is one of:
//
//   - ActionPass: nothing was found, and the texts are delivered as they were;
//   - ActionMask: only credentials were found, and the texts come back with them
//     masked (as {SLACK_TOKEN}, {API_KEY} and so on);
//   - ActionHold: the answer waits for its owner's review, for the Reasons given.
//
// Credentials are taken out of every answer, at every level: Slack's own tokens
// (xoxb-, xoxp-, xapp-, xoxe- and the rest), incoming webhook addresses, and the
// fixed shapes of other keys, beside what Bedrock masks. An answer is held for:
//
//   - at LevelSummary, a run of CopyRun (20) or more words in a row copied word
//     for word from what the run read, or any link (a summary is not the source,
//     and a link is a way to it);
//   - at either level, a run that read nothing to stand on, as the owner decided:
//     an answer that no tool's result backs is held, not delivered on how
//     relevant it sounds;
//   - at either level, a grounding score below what is asked (the answer says
//     something the sources do not), a relevance score below what is asked (it
//     does not answer the question), a denied topic, and a grounding check that
//     could not tell, whether it failed, timed out, or is not set up.
//
// An answer that reports a failure claims nothing about what was read, so it
// needs no sources; an answer its owner wrote themself has no run behind it. Both
// still lose their credentials.
//
// # Words
//
// What counts as a word, and so as 19 or 20 of them, is set by words: runs of
// letters and digits, with a contraction or a number as one word, whatever the
// punctuation, capitals, line breaks and bullets around them, and with invisible
// characters ignored.
//
// # Grounding
//
// Whether an answer stands on its sources is a Grounder's to say. BedrockGrounder
// asks Amazon Bedrock Guardrails' contextual grounding check, with the run's
// tool results as the grounding source, the asker's question as the query, and
// the answer as the content to guard, and reads the scores of its two filters,
// grounding and relevance, and the topics it detected. Bedrock takes 100,000
// characters of source, 1,000 of query and 5,000 of response; the source keeps
// the newest results and the answer is read in pieces.
//
// # Failing closed
//
// Check never fails. A grounding check that errors, times out, returns
// something other than both scores, or is not set up at all holds the answer as
// unchecked, and so does an answer with no source to stand on. Nothing here logs
// a text: a line names what was decided, the kinds, and the scores.
package consentguard
