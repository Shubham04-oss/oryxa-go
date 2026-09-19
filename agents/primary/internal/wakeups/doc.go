// Package wakeups wakes Krude for a member when their connected apps report
// something: batching hands over a member's triggers as a batch, kept here
// all at once, and one run hands them to Krude in the member's own thread for
// them, with the member not there. The size and wait rule here still bounds a
// wake-up, but batching has already decided when, so by default a batch
// wakes Krude as soon as it arrives.
//
// The events are the apps' data, never instructions: Krude reads them,
// tells the member what happened, and changes nothing in their apps without
// their confirmation, which a run they are absent for leaves waiting in the
// thread.
package wakeups
