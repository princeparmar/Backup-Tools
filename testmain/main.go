package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"google.golang.org/api/gmail/v1"
)

func createRawMessage(gmailMsg *gmail.Message) (string, error) {
	var buf bytes.Buffer

	// Write headers
	for _, header := range gmailMsg.Payload.Headers {
		// Skip duplicate headers
		if strings.ToLower(header.Name) == "received" ||
			strings.ToLower(header.Name) == "authentication-results" ||
			strings.ToLower(header.Name) == "received-spf" ||
			strings.ToLower(header.Name) == "x-received" {
			continue
		}
		fmt.Fprintf(&buf, "%s: %s\r\n", header.Name, header.Value)
	}

	// Add boundary for multipart messages
	boundary := "=-" + generateBoundary()
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n", boundary)
	buf.WriteString("\r\n")

	// Write parts
	for _, part := range gmailMsg.Payload.Parts {
		fmt.Fprintf(&buf, "--%s\r\n", boundary)

		// Write part headers
		for _, header := range part.Headers {
			fmt.Fprintf(&buf, "%s: %s\r\n", header.Name, header.Value)
		}
		buf.WriteString("\r\n")

		// Decode and write part body
		if part.Body != nil && part.Body.Data != "" {
			data, err := base64.URLEncoding.DecodeString(part.Body.Data)
			if err != nil {
				return "", fmt.Errorf("failed to decode part body: %v", err)
			}
			buf.Write(data)
			buf.WriteString("\r\n")
		}
	}

	// Close multipart boundary
	fmt.Fprintf(&buf, "--%s--\r\n", boundary)

	// Base64 encode the entire message
	raw := base64.URLEncoding.EncodeToString(buf.Bytes())
	return raw, nil
}

func generateBoundary() string {
	return fmt.Sprintf("lJAfp/%d", time.Now().UnixNano())
}

func main() {
	googleToken := "ya29.a0AeDClZBE_Dib0lpDd0QVbFV5-ToxdAxIgY2HXv4W6WtbV_YGYqbjWhxeTbyRGJoocr7Pwj6U8kCgoU2_UZAViA93ITjUrL1gn5mbsURj6Wv6MXk7CsKNyhuVYCWpyScHc1q_ZuOrofjzT9quGHUZcbnKrP8iBIboUz7Ooa9BaCgYKAU4SARMSFQHGX2MinRZXcwG-0H-cIdl9j7H3yw0175"

	gmailClient, err := google.NewGmailClientUsingToken(googleToken)
	if err != nil {
		panic("auth error: " + err.Error())
	}

	// Your example message JSON
	messageJSON := `{"historyId":"11033907","id":"157799b6faa6e68a","internalDate":"1475213968000","labelIds":["IMPORTANT","CATEGORY_PERSONAL","INBOX"],"payload":{"body":{},"headers":[{"name":"Delivered-To","value":"prince.soamedia@gmail.com"},{"name":"Received","value":"by 10.157.58.116 with SMTP id j107csp65751otc;        Thu, 29 Sep 2016 22:40:12 -0700 (PDT)"},{"name":"X-Received","value":"by 10.157.9.209 with SMTP id 17mr3958217otz.87.1475214012423;        Thu, 29 Sep 2016 22:40:12 -0700 (PDT)"},{"name":"Return-Path","value":"\u003caccount-security-noreply@account.microsoft.com\u003e"},{"name":"Received","value":"from BAY004-OMC3S26.hotmail.com (bay004-omc3s26.hotmail.com. [65.54.190.164])        by mx.google.com with ESMTPS id p84si13274548oif.76.2016.09.29.22.40.12        for \u003cprince.soamedia@gmail.com\u003e        (version=TLS1_2 cipher=ECDHE-RSA-AES128-SHA bits=128/128);        Thu, 29 Sep 2016 22:40:12 -0700 (PDT)"},{"name":"Received-SPF","value":"pass (google.com: domain of account-security-noreply@account.microsoft.com designates 65.54.190.164 as permitted sender) client-ip=65.54.190.164;"},{"name":"Authentication-Results","value":"mx.google.com;       spf=pass (google.com: domain of account-security-noreply@account.microsoft.com designates 65.54.190.164 as permitted sender) smtp.mailfrom=account-security-noreply@account.microsoft.com;       dmarc=pass (p=NONE dis=NONE) header.from=account.microsoft.com"},{"name":"Received","value":"from BN3SCH030020521 ([65.54.190.187]) by BAY004-OMC3S26.hotmail.com over TLS secured channel with Microsoft SMTPSVC(7.5.7601.23008);\t Thu, 29 Sep 2016 22:39:29 -0700"},{"name":"Message-ID","value":"\u003cBN3SCH030020521F6A730B1B6CEA3C6B6B98AC10@phx.gbl\u003e"},{"name":"X-Message-Routing","value":"sKFde7CS5BHygFZaC4gFZWeHmOM+Rjf1iOmv8meDbQqeD+9kHFgbAflrz5UYy6v/Ov/vRliTx0hzi7ScTgwYCoH5DCnfaa3A1uxr6fxWdxBrCBqYWMayiR1cG6DXB/0YS8w6TyqrimIhKRQSTTFIio8LQcw=="},{"name":"Return-Path","value":"account-security-noreply@account.microsoft.com"},{"name":"From","value":"Microsoft account team \u003caccount-security-noreply@account.microsoft.com\u003e"},{"name":"To","value":"prince.soamedia@gmail.com"},{"name":"Date","value":"Thu, 29 Sep 2016 22:39:28 -0700"},{"name":"Subject","value":"Microsoft account password reset"},{"name":"X-Priority","value":"3"},{"name":"X-MSAPipeline","value":"MessageDispatcher"},{"name":"Message-ID","value":"\u003cERILEPYEDZT4.5EZ6VTFCWVAM2@BN3SCH030020521\u003e"},{"name":"X-MSAMetaData","value":"DeYtuwd!7vazlrDwLOtLYinIydDDe*mhuShJ2CVZTJX00Uz6mNFctq0*ySYogBQaw3sArdX6YtSEZAjoofIo3ZWrq32cCZAHr96w0GgtEjHeIHGXC6dnpOP3Vw2eM5tYmg$$"},{"name":"MIME-Version","value":"1.0"},{"name":"Content-Type","value":"multipart/alternative; boundary=\"=-lJAfp/3RMDMLc7Yn1HxdLA==\""},{"name":"X-OriginalArrivalTime","value":"30 Sep 2016 05:39:29.0456 (UTC) FILETIME=[036A5300:01D21ADD]"}],"mimeType":"multipart/alternative","parts":[{"body":{"data":"UGxlYXNlIHVzZSB0aGlzIGNvZGUgdG8gcmVzZXQgdGhlIHBhc3N3b3JkIGZvciB0aGUgTWljcm9zb2Z0IGFjY291bnQgcHIqKioqKkBnbWFpbC5jb20uDQoNCkhlcmUgaXMgeW91ciBjb2RlOiA2MDIxOTA5DQoNCg0KICAgICAgICAgICAgICAgIElmIHlvdSBkb24ndCByZWNvZ25pemUgdGhlIE1pY3Jvc29mdCBhY2NvdW50IHByKioqKipAZ21haWwuY29tLCB5b3UgY2FuIGNsaWNrIGh0dHBzOi8vYWNjb3VudC5saXZlLmNvbS9kcD9mdD1EY1dqaDhyVlBFaW9VUGhUUzhFcTc3aVl3dkNROG13eWh3eHJadipKMWU5Z2ltS3BPWFl0eVVGUEtEeDQ5Z1piNDJ1MXU3KmJ6QUUqU1JNUWp3N3FJTXVaMklOSjI5ZFRwYXRDNEgqRWVlbnhlITUzbm5RdHJEaCphOXhFenQ5RTRnVnUzUVdrd1llZkxWZTkhKk1FZEJEdmplc29OOTJNOUh2amVidUhjcCptTXEqbkRzWnZSMGx2WTJ1anZEZndsZGVaaHlUS2s2d3lUUjRpalJ3Vzc4NnR4VklxZVd0SiFuVXltYmE1SCpsVyoyVzBpajBVYnBMejg5WEtDRDMqTHJNQWFOcFZveFhmNkx1VWVFUkduUVZka1o4IWozdHRqbU84elp0ZFpOSzJBNHB1VkV6ODJFWmt3NXZCMHlZR09nJTI0JTI0IHRvIHJlbW92ZSB5b3VyIGVtYWlsIGFkZHJlc3MgZnJvbSB0aGF0IGFjY291bnQuDQoNClRoYW5rcywNClRoZSBNaWNyb3NvZnQgYWNjb3VudCB0ZWFtIA==","size":664},"headers":[{"name":"Content-Type","value":"text/plain; charset=windows-1252"},{"name":"Content-Transfer-Encoding","value":"8bit"}],"mimeType":"text/plain","partId":"0"},{"body":{"data":"IDwhRE9DVFlQRSBodG1sIFBVQkxJQyAiLS8vVzNDLy9EVEQgWEhUTUwgMS4wIFRyYW5zaXRpb25hbC8vRU4iICJodHRwOi8vd3d3LnczLm9yZy9UUi94aHRtbDEvRFREL3hodG1sMS10cmFuc2l0aW9uYWwuZHRkIj4NCjxodG1sIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8xOTk5L3hodG1sIiBkaXI9Imx0ciI-IA0KPGhlYWQ-DQo8c3R5bGUgdHlwZT0idGV4dC9jc3MiPg0KIC5saW5rOmxpbmssIC5saW5rOmFjdGl2ZSwgLmxpbms6dmlzaXRlZCB7DQogICAgICAgY29sb3I6IzI2NzJlYyAhaW1wb3J0YW50Ow0KICAgICAgIHRleHQtZGVjb3JhdGlvbjpub25lICFpbXBvcnRhbnQ7DQogfQ0KDQogLmxpbms6aG92ZXIgew0KICAgICAgIGNvbG9yOiM0Mjg0ZWUgIWltcG9ydGFudDsNCiAgICAgICB0ZXh0LWRlY29yYXRpb246bm9uZSAhaW1wb3J0YW50Ow0KIH0NCjwvc3R5bGU-DQo8dGl0bGU-PC90aXRsZT4NCjwvaGVhZD4NCjxib2R5Pg0KPHRhYmxlIGRpcj0ibHRyIj4NCiAgICAgIDx0cj48dGQgaWQ9ImkxIiBzdHlsZT0icGFkZGluZzowOyBmb250LWZhbWlseTonU2Vnb2UgVUkgU2VtaWJvbGQnLCAnU2Vnb2UgVUkgQm9sZCcsICdTZWdvZSBVSScsICdIZWx2ZXRpY2EgTmV1ZSBNZWRpdW0nLCBBcmlhbCwgc2Fucy1zZXJpZjsgZm9udC1zaXplOjE3cHg7IGNvbG9yOiM3MDcwNzA7Ij5NaWNyb3NvZnQgYWNjb3VudDwvdGQ-PC90cj4NCiAgICAgIDx0cj48dGQgaWQ9ImkyIiBzdHlsZT0icGFkZGluZzowOyBmb250LWZhbWlseTonU2Vnb2UgVUkgTGlnaHQnLCAnU2Vnb2UgVUknLCAnSGVsdmV0aWNhIE5ldWUgTWVkaXVtJywgQXJpYWwsIHNhbnMtc2VyaWY7IGZvbnQtc2l6ZTo0MXB4OyBjb2xvcjojMjY3MmVjOyI-UGFzc3dvcmQgcmVzZXQgY29kZTwvdGQ-PC90cj4NCiAgICAgIDx0cj48dGQgaWQ9ImkzIiBzdHlsZT0icGFkZGluZzowOyBwYWRkaW5nLXRvcDoyNXB4OyBmb250LWZhbWlseTonU2Vnb2UgVUknLCBUYWhvbWEsIFZlcmRhbmEsIEFyaWFsLCBzYW5zLXNlcmlmOyBmb250LXNpemU6MTRweDsgY29sb3I6IzJhMmEyYTsiPlBsZWFzZSB1c2UgdGhpcyBjb2RlIHRvIHJlc2V0IHRoZSBwYXNzd29yZCBmb3IgdGhlIE1pY3Jvc29mdCBhY2NvdW50IDxhIGRpcj0ibHRyIiBpZD0iaUFjY291bnQiIGNsYXNzPSJsaW5rIiBzdHlsZT0iY29sb3I6IzI2NzJlYzsgdGV4dC1kZWNvcmF0aW9uOm5vbmUiIGhyZWY9Im1haWx0bzpwcioqKioqQGdtYWlsLmNvbSI-cHIqKioqKkBnbWFpbC5jb208L2E-LjwvdGQ-PC90cj4NCiAgICAgIDx0cj48dGQgaWQ9Imk0IiBzdHlsZT0icGFkZGluZzowOyBwYWRkaW5nLXRvcDoyNXB4OyBmb250LWZhbWlseTonU2Vnb2UgVUknLCBUYWhvbWEsIFZlcmRhbmEsIEFyaWFsLCBzYW5zLXNlcmlmOyBmb250LXNpemU6MTRweDsgY29sb3I6IzJhMmEyYTsiPkhlcmUgaXMgeW91ciBjb2RlOiA8c3BhbiBzdHlsZT0iZm9udC1mYW1pbHk6J1NlZ29lIFVJIEJvbGQnLCAnU2Vnb2UgVUkgU2VtaWJvbGQnLCAnU2Vnb2UgVUknLCAnSGVsdmV0aWNhIE5ldWUgTWVkaXVtJywgQXJpYWwsIHNhbnMtc2VyaWY7IGZvbnQtc2l6ZToxNHB4OyBmb250LXdlaWdodDpib2xkOyBjb2xvcjojMmEyYTJhOyI-NjAyMTkwOTwvc3Bhbj48L3RkPjwvdHI-DQogICAgICA8dHI-PHRkIGlkPSJpNSIgc3R5bGU9InBhZGRpbmc6MDsgcGFkZGluZy10b3A6MjVweDsgZm9udC1mYW1pbHk6J1NlZ29lIFVJJywgVGFob21hLCBWZXJkYW5hLCBBcmlhbCwgc2Fucy1zZXJpZjsgZm9udC1zaXplOjE0cHg7IGNvbG9yOiMyYTJhMmE7Ij4NCiAgICAgICAgICAgICAgICANCiAgICAgICAgICAgICAgICBJZiB5b3UgZG9uJ3QgcmVjb2duaXplIHRoZSBNaWNyb3NvZnQgYWNjb3VudCA8YSBkaXI9Imx0ciIgaWQ9ImlBY2NvdW50IiBjbGFzcz0ibGluayIgc3R5bGU9ImNvbG9yOiMyNjcyZWM7IHRleHQtZGVjb3JhdGlvbjpub25lIiBocmVmPSJtYWlsdG86cHIqKioqKkBnbWFpbC5jb20iPnByKioqKipAZ21haWwuY29tPC9hPiwgeW91IGNhbiA8YSBpZD0iaUxpbmsyIiBjbGFzcz0ibGluayIgc3R5bGU9ImNvbG9yOiMyNjcyZWM7IHRleHQtZGVjb3JhdGlvbjpub25lIiBocmVmPSJodHRwczovL2FjY291bnQubGl2ZS5jb20vZHA_ZnQ9RGNXamg4clZQRWlvVVBoVFM4RXE3N2lZd3ZDUThtd3lod3hyWnYqSjFlOWdpbUtwT1hZdHlVRlBLRHg0OWdaYjQydTF1NypiekFFKlNSTVFqdzdxSU11WjJJTkoyOWRUcGF0QzRIKkVlZW54ZSE1M25uUXRyRGgqYTl4RXp0OUU0Z1Z1M1FXa3dZZWZMVmU5ISpNRWRCRHZqZXNvTjkyTTlIdmplYnVIY3AqbU1xKm5Ec1p2UjBsdlkydWp2RGZ3bGRlWmh5VEtrNnd5VFI0aWpSd1c3ODZ0eFZJcWVXdEohblV5bWJhNUgqbFcqMlcwaWowVWJwTHo4OVhLQ0QzKkxyTUFhTnBWb3hYZjZMdVVlRVJHblFWZGtaOCFqM3R0am1POHpadGRaTksyQTRwdVZFejgyRVprdzV2QjB5WUdPZyUyNCUyNCI-Y2xpY2sgaGVyZTwvYT4gdG8gcmVtb3ZlIHlvdXIgZW1haWwgYWRkcmVzcyBmcm9tIHRoYXQgYWNjb3VudC4NCiAgICAgICAgICAgIDwvdGQ-PC90cj4NCiAgICAgIDx0cj48dGQgaWQ9Imk2IiBzdHlsZT0icGFkZGluZzowOyBwYWRkaW5nLXRvcDoyNXB4OyBmb250LWZhbWlseTonU2Vnb2UgVUknLCBUYWhvbWEsIFZlcmRhbmEsIEFyaWFsLCBzYW5zLXNlcmlmOyBmb250LXNpemU6MTRweDsgY29sb3I6IzJhMmEyYTsiPlRoYW5rcyw8L3RkPjwvdHI-DQogICAgICA8dHI-PHRkIGlkPSJpNyIgc3R5bGU9InBhZGRpbmc6MDsgZm9udC1mYW1pbHk6J1NlZ29lIFVJJywgVGFob21hLCBWZXJkYW5hLCBBcmlhbCwgc2Fucy1zZXJpZjsgZm9udC1zaXplOjE0cHg7IGNvbG9yOiMyYTJhMmE7Ij5UaGUgTWljcm9zb2Z0IGFjY291bnQgdGVhbTwvdGQ-PC90cj4NCjwvdGFibGU-DQo8L2JvZHk-DQo8L2h0bWw-","size":2895},"headers":[{"name":"Content-Type","value":"text/html; charset=windows-1252"},{"name":"Content-Transfer-Encoding","value":"8bit"}],"mimeType":"text/html","partId":"1"}]},"sizeEstimate":6111,"snippet":"Microsoft account Password reset code Please use this code to reset the password for the Microsoft account pr*****@gmail.com. Here is your code: 6021909 If you don\u0026#39;t recognize the Microsoft account","threadId":"157799b6faa6e68a"}` // Your full JSON here

	// Parse the message
	var gmailMsg gmail.Message
	if err := json.Unmarshal([]byte(messageJSON), &gmailMsg); err != nil {
		panic("failed to parse message: " + err.Error())
	}

	// Create raw message
	raw, err := createRawMessage(&gmailMsg)
	if err != nil {
		panic("failed to create raw message: " + err.Error())
	}

	// Create new message with raw content
	message := &gmail.Message{
		Raw: raw,
	}

	// Insert the message
	if err := gmailClient.InsertMessage(message); err != nil {
		panic("failed to insert message: " + err.Error())
	}

	fmt.Println("Message inserted successfully")
}
