package answerguard

import "slices"

// Intensity is how much of what the guard finds is acted on before a text is
// shared. The guard reads every text the same way; who is to read the text
// decides how much of what it found is taken out. A text about to go to a
// teammate is masked as strictly as can be; the member who started the work
// may choose to read their own answer with less taken out.
type Intensity string

const (
	// IntensityOff shares the texts exactly as they were given: nothing is
	// masked, and whether they could be checked at all makes no difference.
	IntensityOff Intensity = "off"
	// IntensitySecrets masks only the kinds that are always masked
	// (MaskedKinds: passwords, keys, and card, bank and government numbers).
	// What is held for review at IntensityStrict, an email address, a phone
	// number or an address, is left as written. A text the guard could not
	// check is still not shared: what could not be read may hold a secret.
	IntensitySecrets Intensity = "secrets"
	// IntensityStrict is the rule that holds when nobody has chosen another:
	// what Verdict.Shareable applies. Kinds that are always masked are masked,
	// held kinds are replaced by their kind, and a text that could not be
	// checked is not shared. A level this code does not know is this one.
	IntensityStrict Intensity = "strict"
)

// Outcome is what a verdict comes to for a number of texts at one
// Intensity: what was acted on, and the texts that may be shared.
type Outcome struct {
	// State is StateClear when nothing was acted on, StateMasked when only
	// kinds that are always masked were, and StateHeld for the rest: at
	// IntensityStrict a held kind, whose masked version is what is shared,
	// and, where the guard's word is taken at all, an answer that could not be
	// checked, which shares nothing. IntensityOff acts on nothing, so it is
	// always clear, except for an answer the relevance check held, which is
	// held at every intensity.
	State string
	// Kinds are the kinds acted on at this level, sorted, and never nil: at
	// IntensitySecrets only the ones that are always masked, so a held kind
	// that was left as written is not named; [Unchecked] for an answer that
	// could not be checked; OffTopic, with the categories of what it carried,
	// for an answer the relevance check held. Never a value.
	Kinds []string
	// Shareable are the texts that may be shared, one for each asked for, or
	// nil when none may. It is the one way to tell that nothing may be shared,
	// whatever State says.
	Shareable []string
}

// unreadable is the outcome of a verdict that cannot be shared from: the
// answer could not be checked, or the verdict does not add up.
func unreadable() Outcome {
	return Outcome{State: StateHeld, Kinds: []string{Unchecked}}
}

// At is what the verdict comes to for n texts shared at level, by the rule of
// that level (see Intensity): the state and kinds acted on, and the texts that
// may be shared, or nil when none may. It shares nothing from a verdict that
// does not carry a text for each of the n texts, one in a state this code does
// not know, or, at IntensitySecrets and IntensityStrict, one for an answer
// that could not be checked; none of those is ever read as clear.
//
// An answer the relevance check held is held at every intensity, IntensityOff
// included, and shares nothing: how much personal data is taken out of a text
// is no answer to a text that should not be shared at all.
func (verdict Verdict) At(n int, level Intensity) Outcome {
	if verdict.heldForRelevance() {
		return verdict.relevanceHeld()
	}
	switch level {
	case IntensityOff:
		return verdict.atOff(n)
	case IntensitySecrets:
		return verdict.atSecrets(n)
	default:
		return verdict.atStrict(n)
	}
}

// ShareableAt is the n texts the verdict allows to be shared at level, or nil
// when it allows none; Shareable is it at IntensityStrict.
func (verdict Verdict) ShareableAt(n int, level Intensity) []string {
	return verdict.At(n, level).Shareable
}

// atStrict is Shareable's rule, with what a caller says about it: the
// verdict's own state and kinds when it shares something.
func (verdict Verdict) atStrict(n int) Outcome {
	shareable := verdict.Shareable(n)
	switch {
	case shareable != nil:
		return Outcome{State: verdict.State, Kinds: nonNil(verdict.Kinds), Shareable: shareable}
	case verdict.State == StateHeld && len(verdict.Kinds) > 0:
		// Held for what it says it is held for, with nothing to share instead.
		return Outcome{State: StateHeld, Kinds: slices.Clone(verdict.Kinds)}
	}
	return unreadable()
}

// atOff shares the texts as they were given. A verdict that was not made by
// Check may not know them, and then shares its Texts, which are never more
// than the guard would have shared.
func (verdict Verdict) atOff(n int) Outcome {
	shared := verdict.Given
	if len(shared) != n {
		shared = verdict.Texts
	}
	if len(shared) != n {
		return unreadable()
	}
	return Outcome{State: StateClear, Kinds: []string{}, Shareable: slices.Clone(shared)}
}

// atSecrets masks the kinds that are always masked and nothing else.
//
// Texts are already that: the guardrail masks those kinds in its output and
// leaves the held ones as written. So nothing found, and only always-masked
// kinds found, share Texts, and so does an answer whose findings also include
// held kinds. The exception is an always-masked kind the guardrail left in a
// text (a guardrail no longer set up as it should be), which the answer is
// held for and Texts still holds: then only the fully masked version is known
// to be without it, and that is what is shared, its held values masked too.
func (verdict Verdict) atSecrets(n int) Outcome {
	if len(verdict.Texts) != n {
		return unreadable()
	}
	switch verdict.State {
	case StateClear:
		return Outcome{State: StateClear, Kinds: []string{}, Shareable: slices.Clone(verdict.Texts)}
	case StateMasked:
		return Outcome{
			State: StateMasked, Kinds: nonNil(sortedUnique(append(slices.Clone(verdict.Kinds), verdict.SecretKinds...))),
			Shareable: slices.Clone(verdict.Texts),
		}
	case StateHeld:
		return verdict.heldAtSecrets(n)
	}
	return unreadable()
}

// heldAtSecrets is atSecrets for a held verdict, which names what it is held
// for: held kinds, an always-masked kind the guardrail left in the text, or
// that it could not be checked at all. Nothing else is a reason, so a verdict
// that names none, or one this code does not know, does not add up.
func (verdict Verdict) heldAtSecrets(n int) Outcome {
	if len(verdict.Kinds) == 0 {
		return unreadable()
	}
	secrets := slices.Clone(verdict.SecretKinds)
	left := false
	for _, kind := range verdict.Kinds {
		switch {
		case slices.Contains(HeldKinds(), kind):
		case slices.Contains(MaskedKinds(), kind):
			secrets, left = append(secrets, kind), true
		default:
			// Unchecked, or a kind this code does not know.
			return unreadable()
		}
	}
	texts := verdict.Texts
	if left {
		if len(verdict.Masked) != n {
			return unreadable()
		}
		texts = verdict.Masked
	}
	outcome := Outcome{State: StateClear, Kinds: nonNil(sortedUnique(secrets)), Shareable: slices.Clone(texts)}
	if len(outcome.Kinds) > 0 {
		outcome.State = StateMasked
	}
	return outcome
}

// nonNil is kinds as a list that encodes as one, never null.
func nonNil(kinds []string) []string {
	return append([]string{}, kinds...)
}
