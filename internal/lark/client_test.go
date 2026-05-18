package lark

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/config"
)

type larkAPICallRecorder struct {
	mu               sync.Mutex
	authCount        int
	messageCount     int
	imageUploadCount int
	fileUploadCount  int
	urgentCount      int
	messageTypes     []string
	receiveIDs       []string
	receiveQueries   []string
	postMessages     []string
	textMessages     []string
	urgentQueries    []string
	urgentBodies     []string
}

func TestForwardTelegramAlertTextOnly(t *testing.T) {
	recorder := &larkAPICallRecorder{}
	server := newMockLarkServer(t, recorder)
	defer server.Close()

	client := newTestClient(server.URL)
	alert := &TelegramAlert{
		ChatID:         -1001234567890,
		ChatTitle:      "Alpha Group",
		MessageID:      9876543210,
		MessageType:    "messageText",
		SenderID:       42,
		SenderName:     "Alice",
		SenderUsername: "@alice",
		SentAt:         time.Date(2026, 5, 18, 8, 30, 0, 0, time.UTC),
		Text:           "hello from telegram",
	}

	if err := client.ForwardTelegramAlert(context.Background(), alert); err != nil {
		t.Fatalf("ForwardTelegramAlert returned error: %v", err)
	}

	if recorder.authCount != 1 {
		t.Fatalf("expected 1 auth call, got %d", recorder.authCount)
	}
	if recorder.messageCount != 1 {
		t.Fatalf("expected 1 message call, got %d", recorder.messageCount)
	}
	if recorder.urgentCount != 1 {
		t.Fatalf("expected 1 urgent_app call, got %d", recorder.urgentCount)
	}
	if got := strings.Join(recorder.messageTypes, ","); got != "post" {
		t.Fatalf("expected only post message, got %s", got)
	}
	if len(recorder.postMessages) != 1 {
		t.Fatalf("expected 1 captured post message, got %d", len(recorder.postMessages))
	}
	if !strings.Contains(recorder.postMessages[0], "\"title\":\"Telegram 监控命中\"") {
		t.Fatalf("expected alert post to contain title, got %q", recorder.postMessages[0])
	}
	if !strings.Contains(recorder.postMessages[0], "\"text\":\"群聊：\"") || !strings.Contains(recorder.postMessages[0], "Alpha Group") {
		t.Fatalf("expected alert post to contain chat section, got %q", recorder.postMessages[0])
	}
	if !strings.Contains(recorder.postMessages[0], "\"href\":\"https://t.me/c/1234567890/") {
		t.Fatalf("expected alert post to contain telegram message link, got %q", recorder.postMessages[0])
	}
	if !strings.Contains(recorder.postMessages[0], "\"tag\":\"hr\"") || !strings.Contains(recorder.postMessages[0], "\"text\":\"内容\"") {
		t.Fatalf("expected alert post to contain content section separator, got %q", recorder.postMessages[0])
	}
	if len(recorder.receiveIDs) != 1 || recorder.receiveIDs[0] != "ou_test_user" {
		t.Fatalf("expected direct message to ou_test_user, got %+v", recorder.receiveIDs)
	}
	if len(recorder.receiveQueries) != 1 || !strings.Contains(recorder.receiveQueries[0], "receive_id_type=open_id") {
		t.Fatalf("expected receive_id_type=open_id, got %+v", recorder.receiveQueries)
	}
	if len(recorder.urgentQueries) != 1 || !strings.Contains(recorder.urgentQueries[0], "user_id_type=open_id") {
		t.Fatalf("expected urgent query to contain user_id_type=open_id, got %+v", recorder.urgentQueries)
	}
	if len(recorder.urgentBodies) != 1 || !strings.Contains(recorder.urgentBodies[0], "ou_test_user") {
		t.Fatalf("expected urgent body to contain target user, got %+v", recorder.urgentBodies)
	}
}

func TestForwardTelegramAlertWithImage(t *testing.T) {
	recorder := &larkAPICallRecorder{}
	server := newMockLarkServer(t, recorder)
	defer server.Close()

	client := newTestClient(server.URL)
	imagePath := writeTempFile(t, "sample.png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x0d})
	alert := &TelegramAlert{
		ChatID:         -1001234567890,
		ChatTitle:      "Alpha Group",
		MessageID:      12345,
		MessageType:    "messagePhoto",
		SenderID:       42,
		SenderName:     "Alice",
		SenderUsername: "@alice",
		SentAt:         time.Now().UTC(),
		Text:           "caption text",
		AttachmentType: AttachmentTypeImage,
		AttachmentPath: imagePath,
		AttachmentName: "sample.png",
	}

	if err := client.ForwardTelegramAlert(context.Background(), alert); err != nil {
		t.Fatalf("ForwardTelegramAlert returned error: %v", err)
	}

	if recorder.imageUploadCount != 1 {
		t.Fatalf("expected 1 image upload, got %d", recorder.imageUploadCount)
	}
	if got := strings.Join(recorder.messageTypes, ","); got != "post,image" {
		t.Fatalf("expected post,image messages, got %s", got)
	}
	if recorder.urgentCount != 1 {
		t.Fatalf("expected 1 urgent_app call, got %d", recorder.urgentCount)
	}
}

func TestForwardTelegramAlertWithFile(t *testing.T) {
	recorder := &larkAPICallRecorder{}
	server := newMockLarkServer(t, recorder)
	defer server.Close()

	client := newTestClient(server.URL)
	filePath := writeTempFile(t, "sample.txt", []byte("hello file"))
	alert := &TelegramAlert{
		ChatID:         -1001234567890,
		ChatTitle:      "Alpha Group",
		MessageID:      67890,
		MessageType:    "messageDocument",
		SenderID:       42,
		SenderName:     "Alice",
		SenderUsername: "@alice",
		SentAt:         time.Now().UTC(),
		Text:           "file caption",
		AttachmentType: AttachmentTypeFile,
		AttachmentPath: filePath,
		AttachmentName: "sample.txt",
	}

	if err := client.ForwardTelegramAlert(context.Background(), alert); err != nil {
		t.Fatalf("ForwardTelegramAlert returned error: %v", err)
	}

	if recorder.fileUploadCount != 1 {
		t.Fatalf("expected 1 file upload, got %d", recorder.fileUploadCount)
	}
	if got := strings.Join(recorder.messageTypes, ","); got != "post,file" {
		t.Fatalf("expected post,file messages, got %s", got)
	}
	if recorder.urgentCount != 1 {
		t.Fatalf("expected 1 urgent_app call, got %d", recorder.urgentCount)
	}
}

func TestForwardTelegramAlertToMultipleUsers(t *testing.T) {
	recorder := &larkAPICallRecorder{}
	server := newMockLarkServer(t, recorder)
	defer server.Close()

	client := NewClient(&config.LarkForward{
		Enable:           true,
		AppID:            "cli_test_app",
		AppSecret:        "secret",
		UrgentUserIDType: "open_id",
		UrgentUserIDs:    []string{"ou_user_1", "ou_user_2"},
	}, nil)
	client.baseURL = server.URL

	alert := &TelegramAlert{
		ChatID:         -1001234567890,
		ChatTitle:      "Alpha Group",
		MessageID:      24680,
		MessageType:    "messageText",
		SenderID:       42,
		SenderName:     "Alice",
		SenderUsername: "@alice",
		SentAt:         time.Now().UTC(),
		Text:           "multi target",
	}

	if err := client.ForwardTelegramAlert(context.Background(), alert); err != nil {
		t.Fatalf("ForwardTelegramAlert returned error: %v", err)
	}

	if recorder.messageCount != 2 {
		t.Fatalf("expected 2 direct message calls, got %d", recorder.messageCount)
	}
	if recorder.urgentCount != 2 {
		t.Fatalf("expected 2 urgent_app calls, got %d", recorder.urgentCount)
	}
	if got := strings.Join(recorder.receiveIDs, ","); got != "ou_user_1,ou_user_2" {
		t.Fatalf("expected direct messages to ou_user_1 and ou_user_2, got %s", got)
	}
	if len(recorder.urgentBodies) != 2 || !strings.Contains(recorder.urgentBodies[0], "ou_user_1") || !strings.Contains(recorder.urgentBodies[1], "ou_user_2") {
		t.Fatalf("expected urgent bodies to target matching users, got %+v", recorder.urgentBodies)
	}
}

func TestHumanizeMessageTypeSystemMappings(t *testing.T) {
	testCases := []struct {
		name        string
		messageType string
		want        string
	}{
		{name: "forum closed", messageType: "systemForumTopicClosed", want: "话题已关闭"},
		{name: "forum reopened", messageType: "systemForumTopicReopened", want: "话题已重新打开"},
		{name: "forum hidden", messageType: "systemForumTopicHidden", want: "话题已隐藏"},
		{name: "forum visible", messageType: "systemForumTopicVisible", want: "话题已重新显示"},
		{name: "theme reset", messageType: "systemChatThemeReset", want: "聊天主题已恢复默认"},
		{name: "auto delete disabled", messageType: "systemMessageAutoDeleteDisabled", want: "自动删除已关闭"},
		{name: "join by request", messageType: "messageChatJoinByRequest", want: "通过申请入群"},
		{name: "pin message", messageType: "messagePinMessage", want: "消息已置顶"},
		{name: "forum created", messageType: "messageForumTopicCreated", want: "话题已创建"},
		{name: "payment refunded", messageType: "messagePaymentRefunded", want: "支付已退款"},
		{name: "unknown system", messageType: "messageUnknownFutureType", want: "系统消息"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := humanizeMessageType(testCase.messageType, "")
			if got != testCase.want {
				t.Fatalf("humanizeMessageType(%q) = %q, want %q", testCase.messageType, got, testCase.want)
			}
		})
	}
}

func TestBuildTelegramAlertPostSystemMessageBodyIsHumanized(t *testing.T) {
	alert := &TelegramAlert{
		ChatID:      -1001234567890,
		ChatTitle:   "Alpha Group",
		MessageID:   12345,
		MessageType: "messageChatJoinByLink",
		SenderID:    42,
		SenderName:  "Alice",
		SentAt:      time.Date(2026, 5, 18, 8, 30, 0, 0, time.UTC),
		Text:        "[暂未直接转发附件的消息类型] messageChatJoinByLink",
	}

	post := buildTelegramAlertPost(alert)
	body, err := json.Marshal(post)
	if err != nil {
		t.Fatalf("marshal post: %v", err)
	}

	serialized := string(body)
	if !strings.Contains(serialized, "通过链接入群") {
		t.Fatalf("expected humanized system message label, got %q", serialized)
	}
	if !strings.Contains(serialized, "该消息为 Telegram 系统消息") {
		t.Fatalf("expected humanized system message body, got %q", serialized)
	}
	if strings.Contains(serialized, "[暂未直接转发附件的消息类型] messageChatJoinByLink") {
		t.Fatalf("expected raw message type marker to be removed, got %q", serialized)
	}
}

func TestBuildTelegramAlertPostDetailedSystemMessageLabel(t *testing.T) {
	alert := &TelegramAlert{
		ChatID:      -1001234567890,
		ChatTitle:   "Alpha Group",
		MessageID:   67890,
		MessageType: "systemForumTopicClosed",
		SenderID:    42,
		SenderName:  "Alice",
		SentAt:      time.Date(2026, 5, 18, 8, 30, 0, 0, time.UTC),
		Text:        "该话题已关闭。",
	}

	post := buildTelegramAlertPost(alert)
	body, err := json.Marshal(post)
	if err != nil {
		t.Fatalf("marshal post: %v", err)
	}

	serialized := string(body)
	if !strings.Contains(serialized, "话题已关闭") {
		t.Fatalf("expected detailed system label, got %q", serialized)
	}
	if !strings.Contains(serialized, "该话题已关闭。") {
		t.Fatalf("expected detailed system body text, got %q", serialized)
	}
}

func newTestClient(baseURL string) *Client {
	client := NewClient(&config.LarkForward{
		Enable:           true,
		AppID:            "cli_test_app",
		AppSecret:        "secret",
		UrgentUserIDType: "open_id",
		UrgentUserIDs:    []string{"ou_test_user"},
	}, nil)
	client.baseURL = baseURL
	return client
}

func newMockLarkServer(t *testing.T, recorder *larkAPICallRecorder) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			recorder.authCount++
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"tenant_access_token": "tenant_token",
				"expire":              7200,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages":
			recorder.messageCount++
			body := readJSONBody(t, r)
			receiveID, _ := body["receive_id"].(string)
			recorder.receiveIDs = append(recorder.receiveIDs, receiveID)
			recorder.receiveQueries = append(recorder.receiveQueries, r.URL.RawQuery)
			msgType, _ := body["msg_type"].(string)
			recorder.messageTypes = append(recorder.messageTypes, msgType)
			contentStr, _ := body["content"].(string)
			if msgType == "post" {
				recorder.postMessages = append(recorder.postMessages, contentStr)
			}
			if msgType == "text" {
				content := parseContentJSON(t, body)
				text, _ := content["text"].(string)
				recorder.textMessages = append(recorder.textMessages, text)
			}
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{"message_id": "om_test"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/images":
			recorder.imageUploadCount++
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{"image_key": "img_test"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/files":
			recorder.fileUploadCount++
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{"file_key": "file_test"},
			})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/") && strings.HasSuffix(r.URL.Path, "/urgent_app"):
			recorder.urgentCount++
			recorder.urgentQueries = append(recorder.urgentQueries, r.URL.RawQuery)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read urgent body: %v", err)
			}
			recorder.urgentBodies = append(recorder.urgentBodies, string(body))
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{"invalid_user_id_list": []string{}},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
}

func readJSONBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	defer r.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body
}

func parseContentJSON(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	contentStr, _ := body["content"].(string)
	var content map[string]any
	if err := json.Unmarshal([]byte(contentStr), &content); err != nil {
		t.Fatalf("decode content body: %v", err)
	}
	return content
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func writeTempFile(t *testing.T, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}
