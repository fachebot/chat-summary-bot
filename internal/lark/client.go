package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/fachebot/chat-summary-bot/internal/logger"
)

const (
	defaultBaseURL   = "https://open.larksuite.com"
	maxImageFileSize = 10 * 1024 * 1024
	maxFileSize      = 30 * 1024 * 1024

	AttachmentTypeImage = "image"
	AttachmentTypeFile  = "file"
)

type Client struct {
	httpClient       *http.Client
	baseURL          string
	appID            string
	appSecret        string
	urgentUserIDType string
	urgentUserIDs    []string

	tokenMu       sync.Mutex
	token         string
	tokenExpireAt time.Time
}

type TelegramAlert struct {
	ChatID         int64
	ChatTitle      string
	MessageID      int64
	MessageType    string
	SenderID       int64
	SenderName     string
	SenderUsername string
	SentAt         time.Time
	Text           string
	AttachmentType string
	AttachmentPath string
	AttachmentName string
}

type tokenResponse struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
	Expire            int    `json:"expire"`
}

type apiResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type sendMessageResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		MessageID string `json:"message_id"`
	} `json:"data"`
}

type uploadImageResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ImageKey string `json:"image_key"`
	} `json:"data"`
}

type uploadFileResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		FileKey string `json:"file_key"`
	} `json:"data"`
}

type urgentAppResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		InvalidUserIDList []string `json:"invalid_user_id_list"`
	} `json:"data"`
}

func NewClient(cfg *config.LarkForward, transport *http.Transport) *Client {
	if cfg == nil || !cfg.Enable {
		return nil
	}

	httpClient := &http.Client{Timeout: 45 * time.Second}
	if transport != nil {
		httpClient.Transport = transport.Clone()
	}

	return &Client{
		httpClient:       httpClient,
		baseURL:          defaultBaseURL,
		appID:            cfg.AppID,
		appSecret:        cfg.AppSecret,
		urgentUserIDType: cfg.EffectiveUrgentUserIDType(),
		urgentUserIDs:    cfg.EffectiveUrgentUserIDs(),
	}
}

func (c *Client) ForwardTelegramAlert(ctx context.Context, alert *TelegramAlert) error {
	if c == nil {
		return nil
	}
	if alert == nil {
		return errors.New("lark alert is nil")
	}

	errMessages := make([]string, 0)
	for _, userID := range c.urgentUserIDs {
		if err := c.forwardTelegramAlertToUser(ctx, alert, userID); err != nil {
			errMessages = append(errMessages, fmt.Sprintf("%s: %v", userID, err))
		}
	}
	if len(errMessages) == 0 {
		return nil
	}
	return fmt.Errorf("lark forward failed: %s", strings.Join(errMessages, "; "))
}

func (c *Client) forwardTelegramAlertToUser(ctx context.Context, alert *TelegramAlert, receiveUserID string) error {
	messageID, err := c.sendPostMessage(ctx, receiveUserID, buildTelegramAlertPost(alert))
	if err != nil {
		return err
	}

	urgentErr := c.sendUrgentApp(ctx, messageID, receiveUserID)

	var attachmentErr error
	if alert.AttachmentPath != "" {
		attachmentErr = c.sendAttachment(ctx, receiveUserID, alert)
		if attachmentErr != nil {
			fallback := fmt.Sprintf("附件转发失败: %v", attachmentErr)
			if _, err := c.sendTextMessage(ctx, receiveUserID, fallback); err != nil {
				logger.Warnf("[Lark] 发送附件失败说明也失败: %v", err)
			}
		}
	}

	if attachmentErr != nil && urgentErr != nil {
		return fmt.Errorf("发送附件失败: %w; 应用内加急失败: %v", attachmentErr, urgentErr)
	}
	if attachmentErr != nil {
		return attachmentErr
	}
	return urgentErr
}

type postContent struct {
	ZhCN postLocale `json:"zh_cn"`
}

type postLocale struct {
	Title   string       `json:"title,omitempty"`
	Content [][]postNode `json:"content"`
}

type postNode struct {
	Tag   string   `json:"tag"`
	Text  string   `json:"text,omitempty"`
	Href  string   `json:"href,omitempty"`
	Style []string `json:"style,omitempty"`
}

func buildTelegramAlertPost(alert *TelegramAlert) postContent {
	if alert == nil {
		return postContent{}
	}

	chatTitle := strings.TrimSpace(alert.ChatTitle)
	if chatTitle == "" {
		chatTitle = fmt.Sprintf("<unknown:%d>", alert.ChatID)
	}

	senderName := strings.TrimSpace(alert.SenderName)
	if senderName == "" {
		senderName = fmt.Sprintf("<unknown:%d>", alert.SenderID)
	}

	bodyText := describeAlertBody(alert)

	content := [][]postNode{
		{
			{Tag: "text", Text: "群聊：", Style: []string{"bold"}},
			{Tag: "text", Text: fmt.Sprintf("%s (%d)", chatTitle, alert.ChatID)},
		},
		{
			{Tag: "text", Text: "发送者：", Style: []string{"bold"}},
			{Tag: "text", Text: fmt.Sprintf("%s%s / %d", senderName, formatOptionalUsername(alert.SenderUsername), alert.SenderID)},
		},
		{
			{Tag: "text", Text: "时间：", Style: []string{"bold"}},
			{Tag: "text", Text: alert.SentAt.UTC().Format("2006-01-02 15:04:05 MST")},
		},
		{
			{Tag: "text", Text: "类型：", Style: []string{"bold"}},
			{Tag: "text", Text: humanizeMessageType(alert.MessageType, alert.AttachmentType)},
		},
	}

	if link := buildTelegramMessageLink(alert.ChatID, alert.MessageID); link != "" {
		content = append(content, []postNode{
			{Tag: "text", Text: "原消息：", Style: []string{"bold"}},
			{Tag: "a", Text: "点击查看", Href: link, Style: []string{"bold"}},
		})
	}

	content = append(content,
		[]postNode{{Tag: "hr"}},
		[]postNode{{Tag: "text", Text: "内容", Style: []string{"bold"}}},
		[]postNode{{Tag: "text", Text: bodyText}},
	)

	return postContent{
		ZhCN: postLocale{
			Title:   "Telegram 监控命中",
			Content: content,
		},
	}
}

func formatOptionalUsername(username string) string {
	trimmed := strings.TrimSpace(username)
	if trimmed == "" {
		return ""
	}
	return " (" + trimmed + ")"
}

func humanizeMessageType(messageType, attachmentType string) string {
	switch messageType {
	case "messageText":
		return "文本"
	case "messageSticker":
		return "贴纸"
	case "messagePhoto":
		return "图片"
	case "messageDocument":
		return "文件"
	case "messagePaidMedia":
		return "付费媒体"
	case "messageVideo":
		return "视频"
	case "messageVideoNote":
		return "视频语音"
	case "messageAudio":
		return "音频"
	case "messageVoiceNote":
		return "语音"
	case "messageAnimation":
		return "动画"
	case "messageExpiredPhoto":
		return "已销毁图片"
	case "messageExpiredVideo":
		return "已销毁视频"
	case "messageExpiredVideoNote":
		return "已销毁视频语音"
	case "messageExpiredVoiceNote":
		return "已销毁语音"
	case "messageLocation":
		return "位置"
	case "messageVenue":
		return "地点"
	case "messageContact":
		return "联系人"
	case "messageAnimatedEmoji":
		return "动态表情"
	case "messageDice":
		return "掷骰子"
	case "messageGame":
		return "游戏"
	case "messagePoll":
		return "投票"
	case "messageStory":
		return "故事转发"
	case "messageInvoice":
		return "账单"
	case "messageCall":
		return "通话记录"
	case "messageVideoChatScheduled":
		return "视频聊天已预约"
	case "messageVideoChatStarted":
		return "视频聊天已开始"
	case "messageVideoChatEnded":
		return "视频聊天已结束"
	case "messageInviteVideoChatParticipants":
		return "邀请加入视频聊天"
	case "messageBasicGroupChatCreate":
		return "群组已创建"
	case "messageSupergroupChatCreate":
		return "超级群组已创建"
	case "messageChatChangeTitle":
		return "群标题已修改"
	case "messageChatChangePhoto":
		return "群头像已修改"
	case "messageChatDeletePhoto":
		return "群头像已删除"
	case "messageChatAddMembers":
		return "新成员入群"
	case "messageChatJoinByLink":
		return "通过链接入群"
	case "messageChatJoinByRequest":
		return "通过申请入群"
	case "messageChatDeleteMember":
		return "群成员被移除"
	case "messageChatUpgradeTo":
		return "群组升级为超级群"
	case "messageChatUpgradeFrom":
		return "由普通群升级而来"
	case "messagePinMessage":
		return "消息已置顶"
	case "messageScreenshotTaken":
		return "消息被截图"
	case "messageChatSetBackground":
		return "聊天背景已修改"
	case "messageChatSetTheme":
		return "聊天主题已修改"
	case "messageChatSetMessageAutoDeleteTime":
		return "消息自动删除时间已修改"
	case "messageChatBoost":
		return "群组被助力"
	case "messageForumTopicCreated":
		return "话题已创建"
	case "messageForumTopicEdited":
		return "话题已编辑"
	case "systemForumTopicClosed":
		return "话题已关闭"
	case "systemForumTopicReopened":
		return "话题已重新打开"
	case "messageForumTopicIsClosedToggled":
		return "话题开关已修改"
	case "systemForumTopicHidden":
		return "话题已隐藏"
	case "systemForumTopicVisible":
		return "话题已重新显示"
	case "messageForumTopicIsHiddenToggled":
		return "话题隐藏状态已修改"
	case "systemChatThemeChanged":
		return "聊天主题已切换"
	case "systemChatThemeReset":
		return "聊天主题已恢复默认"
	case "systemMessageAutoDeleteDisabled":
		return "自动删除已关闭"
	case "systemMessageAutoDeleteChanged":
		return "自动删除时间已修改"
	case "messageSuggestProfilePhoto":
		return "推荐了新的头像"
	case "messageCustomServiceAction":
		return "系统服务消息"
	case "messageGameScore":
		return "游戏分数更新"
	case "messagePaymentSuccessful":
		return "支付成功"
	case "messagePaymentSuccessfulBot":
		return "机器人已收款"
	case "messagePaymentRefunded":
		return "支付已退款"
	case "messageGiftedPremium":
		return "赠送了 Telegram Premium"
	case "messagePremiumGiftCode":
		return "Telegram Premium 礼品码"
	case "messageGiveawayCreated":
		return "抽奖已创建"
	case "messageGiveaway":
		return "抽奖消息"
	default:
		if attachmentType == AttachmentTypeImage {
			return "图片"
		}
		if attachmentType == AttachmentTypeFile {
			return "文件"
		}
		if strings.HasPrefix(messageType, "message") {
			return "系统消息"
		}
		return messageType
	}
}

func describeAlertBody(alert *TelegramAlert) string {
	if alert == nil {
		return "[无正文]"
	}

	bodyText := strings.TrimSpace(alert.Text)
	if strings.HasPrefix(bodyText, "[暂未直接转发附件的消息类型]") {
		humanized := humanizeMessageType(alert.MessageType, alert.AttachmentType)
		if humanized == "系统消息" {
			return "该消息为 Telegram 系统消息，当前未转发其原始结构化内容。"
		}
		return fmt.Sprintf("该消息为 Telegram 系统消息：%s。当前未转发其原始结构化内容。", humanized)
	}
	if bodyText != "" {
		return bodyText
	}
	if alert.AttachmentPath != "" {
		return "[无文本，仅附件]"
	}
	if strings.HasPrefix(alert.MessageType, "message") {
		humanized := humanizeMessageType(alert.MessageType, alert.AttachmentType)
		if humanized == "系统消息" {
			return "该消息为 Telegram 系统消息。"
		}
		return fmt.Sprintf("该消息为 Telegram 系统消息：%s。", humanized)
	}
	return "[无正文]"
}

func buildTelegramMessageLink(chatID, messageID int64) string {
	channelID := -chatID - 1000000000000
	if channelID <= 0 || messageID <= 0 {
		return ""
	}
	return fmt.Sprintf("https://t.me/c/%d/%d", channelID, toLinkMessageID(messageID))
}

func toLinkMessageID(messageID int64) int64 {
	const tdlibInternalIDThreshold = 1 << 30
	if messageID >= tdlibInternalIDThreshold {
		return int64(uint64(messageID) >> 20)
	}
	return messageID
}

func (c *Client) sendAttachment(ctx context.Context, receiveUserID string, alert *TelegramAlert) error {
	attachmentName := strings.TrimSpace(alert.AttachmentName)
	if attachmentName == "" {
		attachmentName = filepath.Base(alert.AttachmentPath)
	}

	switch alert.AttachmentType {
	case AttachmentTypeImage:
		imageKey, err := c.uploadImage(ctx, alert.AttachmentPath)
		if err == nil {
			return c.sendImageMessage(ctx, receiveUserID, imageKey)
		}
		logger.Warnf("[Lark] 图片上传失败，降级为文件发送: %v", err)
		return c.sendFileAttachment(ctx, receiveUserID, alert.AttachmentPath, attachmentName)
	case AttachmentTypeFile:
		return c.sendFileAttachment(ctx, receiveUserID, alert.AttachmentPath, attachmentName)
	default:
		return nil
	}
}

func (c *Client) sendTextMessage(ctx context.Context, receiveUserID string, text string) (string, error) {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", err
	}
	return c.sendMessage(ctx, receiveUserID, "text", content)
}

func (c *Client) sendPostMessage(ctx context.Context, receiveUserID string, post postContent) (string, error) {
	content, err := json.Marshal(post)
	if err != nil {
		return "", err
	}
	return c.sendMessage(ctx, receiveUserID, "post", content)
}

func (c *Client) sendImageMessage(ctx context.Context, receiveUserID string, imageKey string) error {
	content, err := json.Marshal(map[string]string{"image_key": imageKey})
	if err != nil {
		return err
	}
	_, err = c.sendMessage(ctx, receiveUserID, "image", content)
	return err
}

func (c *Client) sendFileMessage(ctx context.Context, receiveUserID string, fileKey string) error {
	content, err := json.Marshal(map[string]string{"file_key": fileKey})
	if err != nil {
		return err
	}
	_, err = c.sendMessage(ctx, receiveUserID, "file", content)
	return err
}

func (c *Client) sendMessage(ctx context.Context, receiveUserID string, msgType string, content []byte) (string, error) {
	requestBody, err := json.Marshal(map[string]string{
		"receive_id": receiveUserID,
		"msg_type":   msgType,
		"content":    string(content),
	})
	if err != nil {
		return "", err
	}

	request, err := c.newAuthorizedJSONRequest(ctx, http.MethodPost, "/open-apis/im/v1/messages", url.Values{"receive_id_type": []string{c.urgentUserIDType}}, requestBody)
	if err != nil {
		return "", err
	}

	var response sendMessageResponse
	if err := c.doRequest(request, &response); err != nil {
		return "", err
	}
	if response.Code != 0 {
		return "", fmt.Errorf("lark send message failed: %s", response.Msg)
	}
	if response.Data.MessageID == "" {
		return "", errors.New("lark message_id is empty")
	}
	return response.Data.MessageID, nil
}

func (c *Client) uploadImage(ctx context.Context, filePath string) (string, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return "", err
	}
	if info.Size() > maxImageFileSize {
		return "", fmt.Errorf("图片大小 %d 超过 Lark 上限 %d", info.Size(), maxImageFileSize)
	}

	var response uploadImageResponse
	if err := c.doMultipartUpload(ctx, "/open-apis/im/v1/images", map[string]string{"image_type": "message"}, "image", filePath, &response); err != nil {
		return "", err
	}
	if response.Code != 0 {
		return "", fmt.Errorf("lark upload image failed: %s", response.Msg)
	}
	if response.Data.ImageKey == "" {
		return "", errors.New("lark image_key is empty")
	}
	return response.Data.ImageKey, nil
}

func (c *Client) sendFileAttachment(ctx context.Context, receiveUserID, filePath, fileName string) error {
	fileKey, err := c.uploadFile(ctx, filePath, fileName)
	if err != nil {
		return err
	}
	return c.sendFileMessage(ctx, receiveUserID, fileKey)
}

func (c *Client) uploadFile(ctx context.Context, filePath, fileName string) (string, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return "", err
	}
	if info.Size() > maxFileSize {
		return "", fmt.Errorf("文件大小 %d 超过 Lark 上限 %d", info.Size(), maxFileSize)
	}

	fields := map[string]string{
		"file_type": "stream",
		"file_name": fileName,
	}

	var response uploadFileResponse
	if err := c.doMultipartUpload(ctx, "/open-apis/im/v1/files", fields, "file", filePath, &response); err != nil {
		return "", err
	}
	if response.Code != 0 {
		return "", fmt.Errorf("lark upload file failed: %s", response.Msg)
	}
	if response.Data.FileKey == "" {
		return "", errors.New("lark file_key is empty")
	}
	return response.Data.FileKey, nil
}

func (c *Client) sendUrgentApp(ctx context.Context, messageID string, receiveUserID string) error {
	if strings.TrimSpace(messageID) == "" {
		return errors.New("lark urgent message_id is empty")
	}

	requestBody, err := json.Marshal(map[string]any{
		"user_id_list": []string{receiveUserID},
	})
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/open-apis/im/v1/messages/%s/urgent_app", url.PathEscape(messageID))
	query := url.Values{"user_id_type": []string{c.urgentUserIDType}}
	request, err := c.newAuthorizedJSONRequest(ctx, http.MethodPatch, path, query, requestBody)
	if err != nil {
		return err
	}

	var response urgentAppResponse
	if err := c.doRequest(request, &response); err != nil {
		return err
	}
	if response.Code != 0 {
		return fmt.Errorf("lark send urgent_app failed: %s", response.Msg)
	}
	if len(response.Data.InvalidUserIDList) == 0 {
		return nil
	}

	return fmt.Errorf("部分用户应用内加急失败: %s", strings.Join(response.Data.InvalidUserIDList, "; "))
}

func (c *Client) doMultipartUpload(ctx context.Context, path string, fields map[string]string, fileField, filePath string, out any) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return err
		}
	}

	headers := make(textproto.MIMEHeader)
	headers.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fileField, escapeQuotes(filepath.Base(filePath))))
	headers.Set("Content-Type", detectContentType(filePath))
	part, err := writer.CreatePart(headers)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	request, err := c.newAuthorizedRequest(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())

	return c.doRequest(request, out)
}

func escapeQuotes(value string) string {
	return strings.ReplaceAll(value, `"`, `\\"`)
}

func detectContentType(filePath string) string {
	file, err := os.Open(filePath)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()

	buffer := make([]byte, 512)
	readSize, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return "application/octet-stream"
	}
	return http.DetectContentType(buffer[:readSize])
}

func (c *Client) newAuthorizedJSONRequest(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Request, error) {
	request, err := c.newAuthorizedRequest(ctx, method, path, query, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	return request, nil
}

func (c *Client) newAuthorizedRequest(ctx context.Context, method, path string, query url.Values, body io.Reader) (*http.Request, error) {
	accessToken, err := c.getTenantAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, method, c.buildURL(path, query), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	return request, nil
}

func (c *Client) buildURL(path string, query url.Values) string {
	baseURL := strings.TrimRight(c.baseURL, "/") + path
	if len(query) == 0 {
		return baseURL
	}
	return baseURL + "?" + query.Encode()
}

func (c *Client) getTenantAccessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.token != "" && time.Until(c.tokenExpireAt) > time.Minute {
		return c.token, nil
	}

	requestBody, err := json.Marshal(map[string]string{
		"app_id":     c.appID,
		"app_secret": c.appSecret,
	})
	if err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.buildURL("/open-apis/auth/v3/tenant_access_token/internal", nil), bytes.NewReader(requestBody))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")

	var response tokenResponse
	if err := c.doRequest(request, &response); err != nil {
		return "", err
	}
	if response.Code != 0 {
		return "", fmt.Errorf("lark get tenant_access_token failed: %s", response.Msg)
	}
	if response.TenantAccessToken == "" {
		return "", errors.New("lark tenant_access_token is empty")
	}

	expireIn := time.Duration(response.Expire) * time.Second
	if expireIn <= 0 {
		expireIn = time.Hour
	}
	c.token = response.TenantAccessToken
	c.tokenExpireAt = time.Now().Add(expireIn)
	return c.token, nil
}

func (c *Client) doRequest(request *http.Request, out any) error {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var apiErr apiResponse
		if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Msg != "" {
			return fmt.Errorf("lark http status %d: %s", response.StatusCode, apiErr.Msg)
		}
		return fmt.Errorf("lark http status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode lark response: %w", err)
	}
	return nil
}
