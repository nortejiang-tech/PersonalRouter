# Provider preset references

The UI contains protocol-aware presets so a user can select a known provider
instead of manually searching for every endpoint. A preset is configuration
metadata; it does not log in, save a key, enable an account, or prove that a
model is available to a particular account.

The links below are public vendor documentation used as configuration
references. They are not credentials and do not represent an account that is
configured in any deployment.

| Provider family | Reference |
| --- | --- |
| OpenAI Codex authorization | https://developers.openai.com/codex/auth/ |
| GLM Coding Plan tools | https://docs.bigmodel.cn/cn/coding-plan/tool/others |
| GLM API examples | https://docs.bigmodel.cn/cn/best-practice/case/ai-search-engine |
| Alibaba Model Studio tools | https://help.aliyun.com/zh/model-studio/more-tools |
| Kimi Code | https://www.kimi.com/code/docs/ |
| Kimi provider configuration | https://www.kimi.com/code/docs/en/kimi-code-cli/configuration/providers |
| DeepSeek API | https://api-docs.deepseek.com/ |
| DeepSeek Anthropic API | https://api-docs.deepseek.com/guides/anthropic_api/ |
| DeepSeek Responses API | https://api-docs.deepseek.com/guides/responses_api/ |
| MiniMax OpenAI API | https://platform.minimax.cn/docs/api-reference/text-openai-api |
| MiniMax Anthropic API | https://platform.minimax.cn/docs/api-reference/text-anthropic-api |
| MiniMax Token Plan tools | https://platform.minimax.cn/docs/token-plan/other-tools |

Provider endpoint values and supported protocols can change. Update the preset
implementation only after checking the current official documentation and
adding focused tests for protocol selection, URL normalization, and
unsupported combinations.
