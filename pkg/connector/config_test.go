package connector

import (
	"strings"
	"testing"
	"text/template"
)

func TestRecipients(t *testing.T) {
	params := &ChannelNameParams{recipients: []string{"Ada", "Bela", "Cato", "Dita", "Emil"}}
	for _, test := range []struct{ got, want string }{
		{params.Recipients(), "Ada, Bela, Cato, Dita, Emil"},
		{params.Recipients(0), "Ada, Bela, Cato, Dita, Emil"},
		{params.Recipients(5), "Ada, Bela, Cato, Dita, Emil"},
		{params.Recipients(2), "Ada, Bela +3"},
		{(&ChannelNameParams{}).Recipients(2), ""},
	} {
		if test.got != test.want {
			t.Errorf("got %q, want %q", test.got, test.want)
		}
	}
}

// The limit is only optional if templates can call the method without one.
func TestRecipientsInTemplate(t *testing.T) {
	tpl := template.Must(template.New("channel_name").Parse(`{{.Recipients}}|{{.Recipients 2}}`))
	var buf strings.Builder
	err := tpl.Execute(&buf, &ChannelNameParams{recipients: []string{"Ada", "Bela", "Cato"}})
	if err != nil {
		t.Fatalf("failed to execute template: %v", err)
	}

	got := buf.String()
	want := "Ada, Bela, Cato|Ada, Bela +1"
	if got != want {
		t.Fatalf("unexpected rendered name: got %q, want %q", got, want)
	}
}
