package cli

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestTaskCreateTitleAdmission128BeforeHTTP(t *testing.T) {
	for _, glyph := range []string{"a", "😀"} {
		for _, length := range []int{128, 129} {
			calls := 0
			client := &http.Client{Transport: operationFixtureRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unexpected request")
			})}
			command := newTaskCreateCommand(&options{}, client)
			command.SetArgs([]string{strings.Repeat(glyph, length), "--priority", "prepared-invalid"})
			command.SilenceUsage, command.SilenceErrors = true, true
			want := "priority must be"
			if length == 129 {
				want = "1–128"
			}
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), want) || calls != 0 {
				t.Fatalf("title length %d admission before HTTP: error=%v calls=%d", length, err, calls)
			}
		}
	}
}
