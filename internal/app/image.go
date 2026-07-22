package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "golang.org/x/image/bmp"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	_ "golang.org/x/image/webp"
)

const (
	// maxThumbSide is the longest-edge cap for generated previews. 1200px is
	// plenty for on-screen viewing while keeping payloads tiny.
	maxThumbSide = 1200
	// maxDecodeSide caps the source image dimensions we are willing to decode,
	// so a maliciously huge image cannot exhaust server memory. Anything larger
	// simply falls back to downloading the original.
	maxDecodeSide = 8000
)

// isRasterImage reports whether ext can be rasterized into a thumbnail.
// SVG is deliberately excluded (we do not rasterize it).
func isRasterImage(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

// isLosslessExt reports whether ext should be encoded as PNG (preserving
// transparency) vs JPEG. JPEG sources are re-encoded as JPEG; everything else
// (png/webp/gif/bmp) becomes PNG.
func isLosslessExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".webp", ".gif", ".bmp":
		return true
	}
	return false
}

// generateThumbnail reads the image at abs, scales its longest edge to
// maxThumbSide, and returns the encoded bytes plus the content type. Results
// are cached on disk under a deterministic .thumbs/ sibling directory so
// repeated requests are nearly free.
func (a *App) generateThumbnail(abs string) ([]byte, string, error) {
	ext := strings.ToLower(filepath.Ext(abs))
	outPNG := isLosslessExt(ext)

	// Deterministic cache file name derived from the (immutable) source path.
	sum := sha256.Sum256([]byte(abs))
	cacheBase := "thumb_" + hex.EncodeToString(sum[:])[:16]
	if outPNG {
		cacheBase += ".png"
	} else {
		cacheBase += ".jpg"
	}
	thumbDir := filepath.Join(filepath.Dir(abs), ".thumbs")
	cachePath := filepath.Join(thumbDir, cacheBase)

	if data, ctype, ok := readThumbCache(cachePath, abs, outPNG); ok {
		return data, ctype, nil
	}

	f, err := os.Open(abs)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, "", fmt.Errorf("无法解码图片: %w", err)
	}
	b := img.Bounds()
	if b.Dx() > maxDecodeSide || b.Dy() > maxDecodeSide {
		return nil, "", fmt.Errorf("图片尺寸过大")
	}

	thumb := imaging.Fit(img, maxThumbSide, maxThumbSide, imaging.Lanczos)

	var buf bytes.Buffer
	var ctype string
	if outPNG {
		if err := png.Encode(&buf, thumb); err != nil {
			return nil, "", err
		}
		ctype = "image/png"
	} else {
		if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 82}); err != nil {
			return nil, "", err
		}
		ctype = "image/jpeg"
	}

	// Best-effort cache write; a failure here must not break the response.
	if mkErr := os.MkdirAll(thumbDir, 0755); mkErr == nil {
		_ = os.WriteFile(cachePath, buf.Bytes(), 0644)
	}
	return buf.Bytes(), ctype, nil
}

// readThumbCache returns cached thumbnail bytes if the cache exists and is at
// least as fresh as the original file.
func readThumbCache(cachePath, abs string, outPNG bool) ([]byte, string, bool) {
	info, err := os.Stat(cachePath)
	if err != nil {
		return nil, "", false
	}
	orig, err := os.Stat(abs)
	if err != nil {
		return nil, "", false
	}
	if info.ModTime().Before(orig.ModTime()) {
		return nil, "", false
	}
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, "", false
	}
	ctype := "image/png"
	if !outPNG {
		ctype = "image/jpeg"
	}
	return data, ctype, true
}

// resolveTrackFile validates and resolves the on-disk path of a track file
// (preview or download). noteID==0 addresses the feedback's own files; a
// positive noteID addresses a note's files. Ownership/visibility is enforced
// via the tracking token: a note file is only served when it belongs to the
// token's feedback AND is public, so internal admin attachments never leak.
// Returns the resolved absolute path on success (status 200, empty msg).
func (a *App) resolveTrackFile(c *gin.Context, noteID int64, idx int) (string, int, string) {
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		return "", http.StatusBadRequest, "缺少跟踪令牌"
	}
	fb, err := a.DB.GetFeedbackByTrackingToken(token)
	if err != nil || fb == nil {
		return "", http.StatusNotFound, "未找到对应的反馈记录"
	}

	var paths []string
	if noteID == 0 {
		if err := json.Unmarshal([]byte(fb.FilePaths), &paths); err != nil || len(paths) == 0 {
			return "", http.StatusNotFound, "该反馈没有附件"
		}
	} else {
		note, err := a.DB.GetFeedbackNote(noteID)
		if err != nil || note == nil {
			return "", http.StatusNotFound, "未找到对应的回复"
		}
		if note.FeedbackID != fb.ID {
			return "", http.StatusForbidden, "无权访问该附件"
		}
		if !note.IsPublic {
			// Internal admin notes must never be exposed to submitters.
			return "", http.StatusForbidden, "无权访问该附件"
		}
		if err := json.Unmarshal([]byte(note.FilePaths), &paths); err != nil || len(paths) == 0 {
			return "", http.StatusNotFound, "该回复没有附件"
		}
	}

	if idx < 0 || idx >= len(paths) {
		return "", http.StatusNotFound, "附件不存在"
	}
	return validateUploadPath(a.Cfg.DataDir, paths[idx])
}

// resolveAdminFile validates and resolves the on-disk path of an admin file.
// Admins may view every file (own + note), scoped to feedbacks they are
// permitted to read. Same traversal-safe checks as the track resolver.
func (a *App) resolveAdminFile(c *gin.Context, feedbackID, noteID int64, idx int) (string, int, string) {
	fb, deny := a.checkFeedbackReadPerm(c, feedbackID)
	if deny != "" {
		return "", http.StatusForbidden, deny
	}
	if fb == nil {
		return "", http.StatusNotFound, "反馈不存在"
	}

	var paths []string
	if noteID == 0 {
		if err := json.Unmarshal([]byte(fb.FilePaths), &paths); err != nil || len(paths) == 0 {
			return "", http.StatusNotFound, "该反馈没有附件"
		}
	} else {
		note, err := a.DB.GetFeedbackNote(noteID)
		if err != nil || note == nil {
			return "", http.StatusNotFound, "未找到对应的备注"
		}
		if err := json.Unmarshal([]byte(note.FilePaths), &paths); err != nil || len(paths) == 0 {
			return "", http.StatusNotFound, "该备注没有附件"
		}
	}

	if idx < 0 || idx >= len(paths) {
		return "", http.StatusNotFound, "附件不存在"
	}
	return validateUploadPath(a.Cfg.DataDir, paths[idx])
}

// validateUploadPath turns a DB-stored relative upload path into an absolute,
// symlink-resolved, traversal-checked path rooted at DataDir/uploads.
func validateUploadPath(dataDir, relPath string) (string, int, string) {
	cleaned := filepath.Clean(filepath.FromSlash(relPath))
	if strings.Contains(cleaned, "..") {
		return "", http.StatusForbidden, "非法路径"
	}
	absDataDirResolved, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return "", http.StatusInternalServerError, "路径解析失败"
	}
	absBaseResolved := filepath.Join(absDataDirResolved, "uploads")
	absPath := filepath.Join(absDataDirResolved, cleaned)
	absResolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", http.StatusNotFound, "文件不存在"
	}
	if !strings.HasPrefix(absResolved, absBaseResolved+string(os.PathSeparator)) {
		return "", http.StatusForbidden, "非法路径"
	}
	info, err := os.Stat(absResolved)
	if err != nil || info.IsDir() {
		return "", http.StatusNotFound, "文件不存在"
	}
	return absResolved, http.StatusOK, ""
}

// PublicServeTrackFileThumb serves a small preview image for a track file.
// Auth/visibility rules are identical to PublicServeTrackFile (it reuses the
// same resolver), so previews can never expose more than downloads.
func (a *App) PublicServeTrackFileThumb(c *gin.Context) {
	noteID, _ := strconv.ParseInt(c.Query("note"), 10, 64)
	idx := 0
	if v := c.Query("i"); v != "" {
		idx, _ = strconv.Atoi(v)
	}
	abs, status, msg := a.resolveTrackFile(c, noteID, idx)
	if status != http.StatusOK {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	if !isRasterImage(filepath.Ext(abs)) {
		// SVG / non-image: cannot rasterize → 404, client falls back to download.
		c.JSON(http.StatusNotFound, gin.H{"error": "不支持预览该文件"})
		return
	}
	data, ctype, err := a.generateThumbnail(abs)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "预览生成失败"})
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Data(http.StatusOK, ctype, data)
}

// AdminServeFeedbackThumb serves a small preview image for an admin-viewed
// file (feedback's own files or a note's files). Reuses resolveAdminFile so
// previews inherit the same permission checks as downloads.
func (a *App) AdminServeFeedbackThumb(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	noteID, _ := strconv.ParseInt(c.DefaultQuery("note", "0"), 10, 64)
	idx := 0
	if v := c.Query("i"); v != "" {
		idx, _ = strconv.Atoi(v)
	}
	abs, status, msg := a.resolveAdminFile(c, id, noteID, idx)
	if status != http.StatusOK {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	if !isRasterImage(filepath.Ext(abs)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "不支持预览该文件"})
		return
	}
	data, ctype, err := a.generateThumbnail(abs)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "预览生成失败"})
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Data(http.StatusOK, ctype, data)
}
