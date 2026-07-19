# 6 个 Bug 修复总览

## TL;DR
修复了 FeedShit 中 **4 个权限绕过漏洞** 和 **2 个数据完整性缺陷**，所有修改均在本地完成，无云端上传。

## 修复清单

| Bug | 严重程度 | 文件 | 问题类型 |
|-----|---------|------|---------|
| #4: AdminUnmarkDuplicate 缺写权限检查 | 🔴 高危 | `internal/app/handler_feedback.go` | 权限绕过 |
| #5: UpdateFeedbackStatus 可将状态设为空 | 🟡 中危 | `internal/app/handler_feedback.go` | 数据完整性 |
| #6: AdminUpdateProject 不校验 slug/name | 🟡 中危 | `internal/app/handler_project.go` | 数据完整性 |
| #7: AdminImportCSV 不检查项目写权限 | 🔴 高危 | `internal/app/handler_misc.go`, `handler_report.go` | 权限绕过 |
| #8: AdminImportCSV 不校验 status/priority | 🟡 中危 | `internal/app/handler_report.go` | 数据完整性 |
| #9: ImportFeedback 不生成 content_hash | 🟡 中危 | `internal/database/import.go` | 功能缺陷 |

## 验证状态
- ✅ **编译**: `go build ./...` 通过
- ✅ **单元测试**: `go test ./internal/app/... ./internal/database/... ./internal/middleware/...` 全部通过
