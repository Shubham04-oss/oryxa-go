package judge

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// liveCase is a document whose verdict is not in doubt, for checking a model
// and the instructions against the real thing.
type liveCase struct {
	name      string
	keep      bool
	candidate domain.Candidate
}

func mail(subject, from, labels, bulk, body string) domain.Candidate {
	metadata := map[string]string{"from": from, "to": "me@oryxa.test", "labels": labels}
	if bulk != "" {
		metadata["bulk"] = bulk
	}
	return domain.Candidate{Provider: "gmail", Kind: "message", Title: subject, MIME: transform.MIMEPlain,
		Metadata: metadata, Text: body, Size: len(body)}
}

func file(title, body string) domain.Candidate {
	return domain.Candidate{Provider: "googledrive", Kind: "file", Title: title, MIME: transform.MIMEPlain,
		Metadata: map[string]string{"drive_type": "application/vnd.google-apps.document"}, Text: body, Size: len(body)}
}

// minimalPDF is a one-page PDF showing text, built by hand so the check needs
// no fixture file.
func minimalPDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var out strings.Builder
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for index, object := range objects {
		offsets[index] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(out.String())
}

func pdf(title, text string) domain.Candidate {
	content := minimalPDF(text)
	return domain.Candidate{Provider: "googledrive", Kind: "file", Title: title, MIME: transform.MIMEPDF,
		Metadata: map[string]string{"drive_type": transform.MIMEPDF}, Original: content, Size: len(content)}
}

var liveCases = []liveCase{
	{"a colleague's thread", true, mail("Re: Q3 launch timeline", "Priya Raman <priya@oryxa.test>", "INBOX, IMPORTANT", "",
		"Agreed, let's move the beta to Oct 3 so billing is in. I'll tell design today. Can you own the rollout checklist?")},
	{"an invoice", true, mail("Your invoice INV-2291 from Acme Hosting", "billing@acmehosting.test", "INBOX, CATEGORY_UPDATES", "",
		"Invoice INV-2291. Period: Sep 1-30. 2 x c6g.large: $142.80. Bandwidth: $18.20. Total due Oct 15: $161.00.")},
	{"a booking", true, mail("Booking confirmed: BLR to SFO, 12 Oct", "no-reply@airline.test", "INBOX, CATEGORY_UPDATES", "",
		"Booking reference QX7PLM. Air India AI175, departs Bengaluru 12 Oct 01:40, arrives San Francisco 12 Oct 07:55. Seat 14C.")},
	{"meeting notes", true, file("Interview notes - backend candidate",
		"Strong on Postgres and queues. Designed an outbox pattern at their last job. Weak on Kubernetes. Recommend hire; offer band L4.")},
	{"a flash sale", false, mail("48-hour flash sale: 40% off everything", "Shop <deals@shop.test>", "INBOX, CATEGORY_UPDATES", "yes",
		"Our biggest sale of the season! 40% off sitewide, ends Sunday midnight. Shop now. Unsubscribe.")},
	{"a sign-in alert", false, mail("Security alert: new sign-in on Mac", "no-reply@accounts.google.test", "INBOX, CATEGORY_UPDATES", "",
		"A new sign-in to your account on Mac. If this was you, you don't need to do anything.")},
	{"a verification code", false, mail("Your verification code", "no-reply@service.test", "INBOX", "",
		"Your verification code is 482913. It expires in 10 minutes.")},
	{"a community digest", false, mail("Weekly digest: 12 new posts in your communities", "digest@community.test", "INBOX, CATEGORY_UPDATES", "yes",
		"Top posts this week: 5 tips for productivity, What's your favourite editor?, and 10 more. See all posts.")},
	{"an untitled test document", false, file("Untitled document", "test test")},
	{"an invoice as a PDF", true, pdf("scan_0042.pdf", "Invoice 7781 - Northwind Traders - 3 x consulting days - total USD 4,500 - due 30 Oct")},
}

// TestLiveJudgeDecidesTheObviousCases asks the real model, when
// KNOWLEDGE_JUDGE_LIVE_MODEL names one, about documents whose verdict is not
// in doubt. It runs against Vertex AI in GOOGLE_CLOUD_PROJECT with
// Application Default Credentials, so it is skipped everywhere else.
func TestLiveJudgeDecidesTheObviousCases(t *testing.T) {
	model := os.Getenv("KNOWLEDGE_JUDGE_LIVE_MODEL")
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if model == "" || project == "" {
		t.Skip("KNOWLEDGE_JUDGE_LIVE_MODEL and GOOGLE_CLOUD_PROJECT name the model to check")
	}
	judge, err := New(context.Background(), Config{Project: project, Location: "global", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	verdicts := make([]domain.Verdict, len(liveCases))
	errs := make([]error, len(liveCases))
	for index, c := range liveCases {
		wait.Go(func() { verdicts[index], errs[index] = judge.Judge(context.Background(), c.candidate) })
	}
	wait.Wait()
	for index, c := range liveCases {
		if errs[index] != nil {
			t.Errorf("%s: %v", c.name, errs[index])
			continue
		}
		t.Logf("%-28s keep=%-5t %s", c.name, verdicts[index].Keep, verdicts[index].Reason)
		if verdicts[index].Keep != c.keep {
			t.Errorf("%s: keep = %t, want %t (%s)", c.name, verdicts[index].Keep, c.keep, verdicts[index].Reason)
		}
	}
}
