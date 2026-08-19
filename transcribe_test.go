package main

import "testing"

func TestParseTranscriptionResponse(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "text with detected language",
			body: `{"text":"Hello, world.","languages":[{"code":"en"}]}`,
			want: "Hello, world.",
		},
		{
			name: "empty transcription",
			body: `{"text":"","languages":[]}`,
		},
		{
			name:    "missing text",
			body:    `{"languages":[]}`,
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			body:    `not JSON`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTranscriptionResponse(tt.body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseTranscriptionResponse() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseTranscriptionResponse() = %q, want %q", got, tt.want)
			}
		})
	}
}
