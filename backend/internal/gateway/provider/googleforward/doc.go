// Package googleforward 执行 Gemini 和 Antigravity 的单次转发与同提供商恢复。
//
// 阅读入口：
//   - gemini_attempts.go：处理 Gemini Messages 和 GenerateContent 入口。
//   - antigravity_messages.go：处理 Antigravity Messages 转换与重试。
//
// 文件分组：
//   - gemini_*.go：Gemini 转发、OpenAI 兼容转换和响应处理。
//   - antigravity_*.go：Antigravity 请求转换、签名恢复和响应处理。
//   - model_attributes.go：根据解析后的模型属性编码采样与推理参数。
package googleforward
