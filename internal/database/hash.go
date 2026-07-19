package database

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// normalizeContent 归一化内容用于指纹计算：
// 去首尾空白 → 小写 → 去除 Unicode 标点（保留空格）→ 多空白折叠为单空格。
func normalizeContent(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPunct(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ComputeContentHash 由标题+描述计算内容指纹。
// 空描述时不附加多余空格，避免尾随空格导致与查询侧不一致。
func ComputeContentHash(title, body string) string {
	parts := []string{normalizeContent(title)}
	if nb := normalizeContent(body); nb != "" {
		parts = append(parts, nb)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, " ")))
	return hex.EncodeToString(sum[:])
}

// ComputeContentHashFromText 由「标题+描述」拼接串计算指纹（与提交页 q 一致）。
// 因 normalizeContent 保留空格分隔并折叠多空白，与 ComputeContentHash 归一化结果一致。
func ComputeContentHashFromText(text string) string {
	sum := sha256.Sum256([]byte(normalizeContent(text)))
	return hex.EncodeToString(sum[:])
}
