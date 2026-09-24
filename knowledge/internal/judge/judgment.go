package judge

import (
	"encoding/json"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// judgment is what a judge keeps with a document's verdict: each judge's own
// answer, from which the verdict can be drawn again for other kinds of
// document without asking again.
type judgment struct {
	// Jev is Jev's classification, when Jev was asked.
	Jev *Classification `json:"jev,omitempty"`
	// Gemini is the Gemini judge's verdict, when it was asked: for a file Jev
	// cannot read, or a document Jev was unsure of.
	Gemini *geminiVerdict `json:"gemini,omitempty"`
}

type geminiVerdict struct {
	Keep     bool   `json:"keep"`
	Reason   string `json:"reason"`
	Category string `json:"category,omitempty"`
	Model    string `json:"model,omitempty"`
}

func (j judgment) encode() []byte {
	encoded, err := json.Marshal(j)
	if err != nil {
		return nil
	}
	return encoded
}

// decodeJudgment reads a kept judgment; one that cannot be read is empty,
// and decides nothing.
func decodeJudgment(kept []byte) judgment {
	var given judgment
	if len(kept) > 0 {
		_ = json.Unmarshal(kept, &given)
	}
	return given
}

// gemini is the verdict the Gemini judge gave, if it was asked.
func (j judgment) gemini(kept []byte) (domain.Verdict, bool) {
	if j.Gemini == nil {
		return domain.Verdict{}, false
	}
	return domain.Verdict{Keep: j.Gemini.Keep, Reason: j.Gemini.Reason, Category: j.category(), Judgment: kept}, true
}

// category is what kind of document the judgment says this is. The Gemini
// judge's answer wins when it gave one: it was asked because Jev could not
// say, or could not read the document at all. Jev's own kind stands
// otherwise. It depends on nothing but the judgment, so every verdict drawn
// from it, whichever kinds a source indexes, names the same category.
func (j judgment) category() string {
	if j.Gemini != nil && j.Gemini.Category != "" {
		return j.Gemini.Category
	}
	if j.Jev != nil {
		return j.Jev.Kind
	}
	return ""
}
