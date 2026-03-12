package m365

import (
	"context"

	sdktr "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// 本文件负责把 OpenAI Responses（openai-response）与本 provider（m365）在 SDK translator 层面关联起来。
// 当前实现选择“透传”：Executor 直接产出 OpenAI Responses SSE chunk，translator 不再二次转换。

func init() {
	sdktr.Register(
		sdktr.FormatOpenAIResponse,
		sdktr.Format(providerKey),
		// 请求转换：透传
		func(_ string, raw []byte, _ bool) []byte { return raw },
		// 响应转换：透传
		sdktr.ResponseTransform{
			Stream: func(_ context.Context, _ string, _ []byte, _ []byte, raw []byte, _ *any) []string {
				return []string{string(raw)}
			},
			NonStream: func(_ context.Context, _ string, _ []byte, _ []byte, raw []byte, _ *any) string {
				return string(raw)
			},
		},
	)
}
