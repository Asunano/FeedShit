package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"

	"feedshit/internal/database"
)

// svgDangerousPatterns matches script tags and event handler attributes in SVG content.
var svgScriptTag = regexp.MustCompile(`(?i)<script[\s\S]*?</script>`)
var svgEventAttr = regexp.MustCompile(`(?i)\s+on\w+\s*=\s*"[^"]*"`)
var svgEventAttrSingle = regexp.MustCompile(`(?i)\s+on\w+\s*=\s*'[^']*'`)
var svgJavascriptURL = regexp.MustCompile(`(?i)(href|xlink:href)\s*=\s*["']?\s*javascript:`)
var svgEventAttrNoQuote = regexp.MustCompile(`(?i)\s+on\w+\s*=\s*[^\s>"']+`)
var svgUseElement = regexp.MustCompile(`(?i)<use[\s>][\s\S]*?</use>|<use[\s>][^>]*/?>`)
var svgForeignObject = regexp.MustCompile(`(?i)<foreignObject[\s>][\s\S]*?</foreignObject>`)
var svgNestedNamespace = regexp.MustCompile(`(?i)xmlns\s*=\s*["']?[^"'\s]*javascript`)

// truncateRunes truncates s to at most n runes, appending "..." if truncated.
// Unlike byte-level slicing, this never splits multi-byte UTF-8 characters.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// detectWebhookPlatform returns the platform name based on the webhook URL.
func detectWebhookPlatform(url string) string {
	switch {
	case strings.Contains(url, "open.feishu.cn") || strings.Contains(url, "feishu"):
		return "feishu"
	case strings.Contains(url, "oapi.dingtalk.com"):
		return "dingtalk"
	case strings.Contains(url, "hooks.slack.com"):
		return "slack"
	case strings.Contains(url, "qyapi.weixin.qq.com"):
		return "wecom"
	default:
		return "generic"
	}
}

// sanitizeSVG removes dangerous elements and attributes from SVG content.
func sanitizeSVG(content string) string {
	// Remove <script>...</script> blocks
	content = svgScriptTag.ReplaceAllString(content, "")
	// Remove event handler attributes (onload, onerror, onclick, etc.)
	content = svgEventAttr.ReplaceAllString(content, "")
	content = svgEventAttrSingle.ReplaceAllString(content, "")
	content = svgEventAttrNoQuote.ReplaceAllString(content, "")
	// Remove javascript: URLs
	content = svgJavascriptURL.ReplaceAllString(content, `data-removed-href="`)
	// Remove <use> elements (external resource injection)
	content = svgUseElement.ReplaceAllString(content, "")
	// Remove <foreignObject> elements (nested XSS vector)
	content = svgForeignObject.ReplaceAllString(content, "")
	// Remove javascript: namespace declarations
	content = svgNestedNamespace.ReplaceAllString(content, `data-removed-xmlns="`)
	return content
}

// validateFileContent checks file magic bytes to ensure content matches extension.
func validateFileContent(header []byte, ext string) bool {
	switch ext {
	case ".png":
		return len(header) >= 4 && header[0] == 0x89 && header[1] == 0x50 && header[2] == 0x4E && header[3] == 0x47
	case ".jpg", ".jpeg":
		return len(header) >= 2 && header[0] == 0xFF && header[1] == 0xD8
	case ".gif":
		return len(header) >= 3 && string(header[:3]) == "GIF"
	case ".webp":
		return len(header) >= 12 && string(header[8:12]) == "WEBP"
	case ".bmp":
		return len(header) >= 2 && header[0] == 0x42 && header[1] == 0x4D
	case ".svg":
		content := strings.TrimSpace(string(header))
		return strings.HasPrefix(content, "<") && (strings.Contains(content, "<svg") || strings.Contains(content, "<?xml"))
	case ".json":
		trimmed := bytes.TrimLeft(header, "\xef\xbb\xbf \t\r\n")
		return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
	case ".log", ".txt", ".csv":
		return true // text files — no reliable magic bytes
	case ".pdf":
		return len(header) >= 5 && string(header[:5]) == "%PDF-"
	case ".zip", ".docx":
		// ZIP local file header; .docx/.xlsx/.pptx are ZIP containers sharing this magic.
		return len(header) >= 4 && header[0] == 0x50 && header[1] == 0x4B && header[2] == 0x03 && header[3] == 0x04
	case ".doc":
		// OLE2 compound document magic (legacy .doc).
		return len(header) >= 8 &&
			header[0] == 0xD0 && header[1] == 0xCF && header[2] == 0x11 && header[3] == 0xE0 &&
			header[4] == 0xA1 && header[5] == 0xB1 && header[6] == 0x1A && header[7] == 0xE1
	default:
		return false
	}
}

// eventTitle returns a human-readable title for a webhook event.
func eventTitle(event string) string {
	switch event {
	case "new_feedback":
		return "新反馈"
	case "status_change":
		return "状态变更"
	case "new_note":
		return "新增备注"
	case "priority_change":
		return "优先级变更"
	case "assignee_change":
		return "指派变更"
	default:
		return event
	}
}

// buildFeishuCard builds a Feishu interactive card message.
func buildFeishuCard(event string, data map[string]interface{}, fb *database.Feedback) ([]byte, error) {
	title := fmt.Sprintf("%s - %v", eventTitle(event), data["title"])
	id := data["id"]
	card := map[string]interface{}{
		"msg_type": "interactive",
		"card": map[string]interface{}{
			"header": map[string]interface{}{
				"title": map[string]string{
					"tag":     "plain_text",
					"content": title,
				},
				"template": "blue",
			},
			"elements": []map[string]interface{}{
				{
					"tag": "div",
					"fields": []map[string]interface{}{
						{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**项目：** %v", data["project_id"])}},
						{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**编号：** #%v", id)}},
					},
				},
			},
		},
	}

	// Add description if available
	if desc, ok := data["description"].(string); ok && desc != "" {
		desc = truncateRunes(desc, 200)
		elements := card["card"].(map[string]interface{})["elements"].([]map[string]interface{})
		elements = append(elements, map[string]interface{}{
			"tag": "div",
			"text": map[string]string{
				"tag":     "lark_md",
				"content": html.EscapeString(desc),
			},
		})
		card["card"].(map[string]interface{})["elements"] = elements
	}

	// Add status/priority if from feedback
	if fb != nil {
		elements := card["card"].(map[string]interface{})["elements"].([]map[string]interface{})
		statusLabels := database.StatusLabels
		priorityLabels := map[string]string{"urgent": "🔴 紧急", "high": "🟡 高", "medium": "🔵 中", "low": "⚪ 低"}
		fields := []map[string]interface{}{
			{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**状态：** %s", statusLabels[fb.Status])}},
		}
		if fb.Priority != "" {
			fields = append(fields, map[string]interface{}{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**优先级：** %s", priorityLabels[fb.Priority])}})
		}
		elements = append(elements, map[string]interface{}{"tag": "div", "fields": fields})
		card["card"].(map[string]interface{})["elements"] = elements
	}

	return json.Marshal(card)
}

// buildDingTalkCard builds a DingTalk markdown message.
func buildDingTalkCard(event string, data map[string]interface{}, fb *database.Feedback) ([]byte, error) {
	title := fmt.Sprintf("%s - %v", eventTitle(event), data["title"])
	var md strings.Builder
	md.WriteString(fmt.Sprintf("### %s\n\n", title))
	md.WriteString(fmt.Sprintf("- **编号：** #%v\n", data["id"]))
	md.WriteString(fmt.Sprintf("- **项目：** %v\n", data["project_id"]))
	if desc, ok := data["description"].(string); ok && desc != "" {
		desc = truncateRunes(desc, 200)
		md.WriteString(fmt.Sprintf("- **描述：** %s\n", desc))
	}
	if fb != nil {
		statusLabels := database.StatusLabels
		md.WriteString(fmt.Sprintf("- **状态：** %s\n", statusLabels[fb.Status]))
		if fb.Priority != "" {
			priorityLabels := map[string]string{"urgent": "紧急", "high": "高", "medium": "中", "low": "低"}
			md.WriteString(fmt.Sprintf("- **优先级：** %s\n", priorityLabels[fb.Priority]))
		}
	}

	payload := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": title,
			"text":  md.String(),
		},
	}
	return json.Marshal(payload)
}

// buildSlackCard builds a Slack Block Kit message.
func buildSlackCard(event string, data map[string]interface{}, fb *database.Feedback) ([]byte, error) {
	title := fmt.Sprintf("%s - %v", eventTitle(event), data["title"])
	blocks := []map[string]interface{}{
		{
			"type": "header",
			"text": map[string]string{"type": "plain_text", "text": title},
		},
		{
			"type": "section",
			"fields": []map[string]interface{}{
				{"type": "mrkdwn", "text": fmt.Sprintf("*编号:*\n#%v", data["id"])},
				{"type": "mrkdwn", "text": fmt.Sprintf("*项目:*\n%v", data["project_id"])},
			},
		},
	}

	if desc, ok := data["description"].(string); ok && desc != "" {
		desc = truncateRunes(desc, 500)
		blocks = append(blocks, map[string]interface{}{
			"type": "section",
			"text": map[string]string{"type": "mrkdwn", "text": desc},
		})
	}

	if fb != nil {
		statusLabels := database.StatusLabels
		fields := []map[string]interface{}{
			{"type": "mrkdwn", "text": fmt.Sprintf("*状态:*\n%s", statusLabels[fb.Status])},
		}
		if fb.Priority != "" {
			priorityLabels := map[string]string{"urgent": "🔴 紧急", "high": "🟡 高", "medium": "🔵 中", "low": "⚪ 低"}
			fields = append(fields, map[string]interface{}{"type": "mrkdwn", "text": fmt.Sprintf("*优先级:*\n%s", priorityLabels[fb.Priority])})
		}
		blocks = append(blocks, map[string]interface{}{"type": "section", "fields": fields})
	}

	payload := map[string]interface{}{
		"text":   title,
		"blocks": blocks,
	}
	return json.Marshal(payload)
}

// buildWeComCard builds a WeCom (企业微信) markdown message.
func buildWeComCard(event string, data map[string]interface{}, fb *database.Feedback) ([]byte, error) {
	title := fmt.Sprintf("%s - %v", eventTitle(event), data["title"])
	var md strings.Builder
	md.WriteString(fmt.Sprintf("## %s\n\n", title))
	md.WriteString(fmt.Sprintf("> 编号: **#%v**\n", data["id"]))
	md.WriteString(fmt.Sprintf("> 项目: **%v**\n", data["project_id"]))
	if desc, ok := data["description"].(string); ok && desc != "" {
		desc = truncateRunes(desc, 200)
		md.WriteString(fmt.Sprintf("> 描述: %s\n", desc))
	}
	if fb != nil {
		statusLabels := database.StatusLabels
		md.WriteString(fmt.Sprintf("> 状态: <font color=\"info\">%s</font>\n", statusLabels[fb.Status]))
		if fb.Priority != "" {
			priorityLabels := map[string]string{"urgent": "紧急", "high": "高", "medium": "中", "low": "低"}
			md.WriteString(fmt.Sprintf("> 优先级: %s\n", priorityLabels[fb.Priority]))
		}
	}

	payload := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": md.String(),
		},
	}
	return json.Marshal(payload)
}
