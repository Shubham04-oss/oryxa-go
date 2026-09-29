// Package relevance decides whether an answer one member's agent gives
// another member carries private material that the question did not ask for.
//
// Text from the other side can steer an agent into reading its owner's
// private apps (mail, messages, files, memories) and repeating something
// unrelated to what was asked. Masking (package answerguard) catches personal
// data and credentials by their shape; it cannot tell that a sentence about a
// colleague's surgery has nothing to do with a question about when a meeting
// is. That takes reading the question and the answer together. The owner's
// rule is: if something private is required except work, it goes behind
// classifiers and a model that checks the request and the output match;
// otherwise it is held for the owner to review.
//
// # How it decides
//
// A Judge is given an Input: the request in the asker's words, a few lines of
// the conversation around it, the answer's texts, and the names of the tools
// the run called. It answers with a Result, in three steps:
//
//  1. The provenance gate, NeedsJudging: an answer can only carry private
//     material if the run read some, and a run whose tools all read nothing
//     private (a directory lookup, a calculation) costs no model call. A tool
//     the package has never heard of counts as private, so a new tool is
//     judged until someone decides otherwise.
//  2. One call to a small model (NewGemini), at temperature zero, which
//     names categories and nothing else: the private ones the request asked
//     for (Asked), and the ones the answer carries that it did not ask for,
//     at the breadth it asked (Extras). "Is she free Thursday 2-3?" is
//     answered by a yes or no and a time; a medical reason for a no is a
//     Health extra. Work is never a category: names, dates and money in a
//     work sense are what answers are made of.
//  3. ShouldHold, in code: an answer is held for an error, for any extra,
//     and, when the organization is regulated, for a request that asked for
//     health, HR or compensation. The model's certainty is logged and never
//     decides. Nothing about when to hold is in the prompt, so no wording a
//     model is given can move it.
//
// # Failing closed
//
// A judge that cannot tell (a timeout, an error, an answer that is not the
// JSON asked for, a category that does not exist, a model that would not read
// the text) returns Err with Hold set, and the caller holds the answer as
// UNCHECKED. It does not retry, and its Err says why without repeating the
// model's own message, which can quote what it was sent. Nothing here logs a
// text: LogFields gives the fields of a Result that are safe to log.
//
// # What a caller must do
//
//   - Pass the tools the run called, from what the run recorded. The agent's
//     own report of them can leave one out, which is what steering does.
//   - Pass MemoryPreload when the run had memories placed in its context, and
//     ThreadHistory when it started on a thread of the member's own earlier
//     turns: private material no tool name says it read.
//   - Hold when Hold is true, and treat Err as UNCHECKED. Read Truncated: the
//     judge is shown at most MaxRequestRunes of a request, MaxContextLines
//     lines of MaxContextLineRunes, and MaxAnswerRunes of an answer, keeping
//     the start and the end of what is longer, and what it was not shown it
//     did not judge.
//
// # Judging is not proof
//
// The answer is untrusted text read by a model, and a model can be argued
// with: the data goes in blocks whose markers carry a code nobody can know
// beforehand, and the instruction says that text inside them, addressed to
// the judge or to the assistant, is never to be followed. That is a defence,
// not a guarantee. The evaluation in relevance/eval holds the judge to
// the numbers: at most one clean answer in a hundred held, at least 95% of
// leaks caught, every injection caught, and a 95th percentile under four
// seconds. Run it before changing the instruction, the model or its thinking
// level.
//
// Nothing calls this package yet: it is the judge and its measurements, for a
// later change to wire in front of an answer another member reads.
package relevance
