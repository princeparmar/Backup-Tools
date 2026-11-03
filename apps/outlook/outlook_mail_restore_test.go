package outlook

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateMailBackupForRestore_table(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{
			name: "graph mail with body",
			raw:  `{"subject":"Hello","body":{"contentType":"html","content":"<p>x</p>"},"from":{"emailAddress":{"address":"a@b.c"}}}`,
		},
		{
			name: "subject only still restorable",
			raw:  `{"subject":"No body yet","body":{"contentType":"text","content":""}}`,
		},
		{
			name: "attachment only restorable",
			raw: `{
				"subject":"",
				"body":{"contentType":"text","content":""},
				"attachments":[{"@odata.type":"#microsoft.graph.fileAttachment","name":"a.txt","contentBytes":"eA=="}]
			}`,
		},
		{
			name:    "empty object",
			raw:     "",
			wantErr: "empty",
		},
		{
			name:    "not json",
			raw:     "not-json",
			wantErr: "parse",
		},
		{
			name:    "no restorable fields",
			raw:     `{"id":"M1","changeKey":"ck","storx_backup":{"folder_path":"Inbox"}}`,
			wantErr: "no subject",
		},
		{
			name: "legacy snake_case",
			raw:  `{"subject":"Old","body":"plain text","to_recipients":["a@b.c"],"from":"a@b.c","received_datetime":"2026-01-02T03:04:05Z"}`,
		},
		{
			name: "body as bare string normalized later",
			raw:  `{"subject":"S","body":"hello from string body","from":{"emailAddress":{"address":"a@b.c"}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMailBackupForRestore([]byte(tc.raw))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantErr)) {
				t.Fatalf("err=%v want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestEnsureMailRestoreBody_table(t *testing.T) {
	cases := []struct {
		name        string
		bodyRaw     string
		wantType    string
		wantContent string
	}{
		{
			name:        "missing body becomes empty text",
			bodyRaw:     "",
			wantType:    "text",
			wantContent: "",
		},
		{
			name:        "adds contentType when missing",
			bodyRaw:     `{"content":"<b>hi</b>"}`,
			wantType:    "text",
			wantContent: "<b>hi</b>",
		},
		{
			name:        "lowercases contentType",
			bodyRaw:     `{"contentType":"HTML","content":"<p>x</p>"}`,
			wantType:    "html",
			wantContent: "<p>x</p>",
		},
		{
			name:        "bare string body",
			bodyRaw:     `"hello"`,
			wantType:    "text",
			wantContent: "hello",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := &mailRestoreMessage{Fields: map[string]json.RawMessage{}}
			if tc.bodyRaw != "" {
				msg.Fields["body"] = json.RawMessage(tc.bodyRaw)
			}
			if err := ensureMailRestoreBody(msg); err != nil {
				t.Fatal(err)
			}
			var body struct {
				ContentType string `json:"contentType"`
				Content     string `json:"content"`
			}
			if err := json.Unmarshal(msg.Fields["body"], &body); err != nil {
				t.Fatal(err)
			}
			if body.ContentType != tc.wantType || body.Content != tc.wantContent {
				t.Fatalf("got %+v want type=%q content=%q", body, tc.wantType, tc.wantContent)
			}
		})
	}
}

func TestBuildMailRestoreCreatePayload_stripsFromWhenAsked(t *testing.T) {
	msg := &mailRestoreMessage{
		Fields: map[string]json.RawMessage{
			"subject": json.RawMessage(`"s"`),
			"from":    json.RawMessage(`{"emailAddress":{"address":"a@b.c"}}`),
			"sender":  json.RawMessage(`{"emailAddress":{"address":"a@b.c"}}`),
			"body":    json.RawMessage(`{"contentType":"text","content":"x"}`),
		},
		IsRead:   true,
		Received: "2026-01-02T03:04:05Z",
	}
	full := buildMailRestoreCreatePayload(msg, nil, true, true)
	if _, ok := full["from"]; !ok {
		t.Fatal("full payload should keep from")
	}
	if _, ok := full["singleValueExtendedProperties"]; !ok {
		t.Fatal("full payload should keep extended props")
	}
	noFrom := buildMailRestoreCreatePayload(msg, nil, false, true)
	if _, ok := noFrom["from"]; ok {
		t.Fatal("no-from payload still has from")
	}
	if _, ok := noFrom["sender"]; ok {
		t.Fatal("no-from payload still has sender")
	}
	minimal := buildMailRestoreCreatePayload(msg, nil, false, false)
	if _, ok := minimal["singleValueExtendedProperties"]; ok {
		t.Fatal("minimal payload still has extended props")
	}
}

func TestMailCreateRetryHeuristics_table(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		retryFrom  bool
		retryExt   bool
	}{
		{
			name:      "access denied on from",
			status:    403,
			body:      `{"error":{"code":"ErrorAccessDenied","message":"Access is denied."}}`,
			retryFrom: true,
		},
		{
			name:      "send as required",
			status:    400,
			body:      `{"error":{"code":"ErrorInvalidRequest","message":"The user needs Send As permission"}}`,
			retryFrom: true,
		},
		{
			name:     "extended property rejected",
			status:   400,
			body:     `{"error":{"code":"ErrorInvalidProperty","message":"singleValueExtendedProperties not allowed"}}`,
			retryExt: true,
		},
		{
			name:   "unrelated 500 does not retry",
			status: 500,
			body:   `{"error":{"code":"ErrorServerBusy","message":"busy"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mailCreateShouldRetryWithoutFrom(tc.status, []byte(tc.body)); got != tc.retryFrom {
				t.Fatalf("retryFrom=%v want %v", got, tc.retryFrom)
			}
			if got := mailCreateShouldRetryWithoutExtended(tc.status, []byte(tc.body)); got != tc.retryExt {
				t.Fatalf("retryExt=%v want %v", got, tc.retryExt)
			}
		})
	}
}
