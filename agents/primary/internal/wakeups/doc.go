// Package wakeups wakes Krude for a member when their connected apps report
// something: trigger events collect per member, and once enough have come,
// or the oldest has waited long enough, one run hands them all to Krude in
// the member's own thread for them, with the member not there.
//
// The events are the apps' data, never instructions: Krude reads them,
// tells the member what happened, and changes nothing in their apps without
// their confirmation, which a run they are absent for leaves waiting in the
// thread.
package wakeups
