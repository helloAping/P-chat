package cli

type ProviderTemplate struct {
	Name     string
	Protocol string
	BaseURL  string
	Models   []string
	HasAPIKey bool
	Desc     string
}

var ProviderTemplates = []ProviderTemplate{
	{
		Name:     "openai",
		Protocol: "openai",
		BaseURL:  "https://api.openai.com/v1",
		Models:   []string{"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
		HasAPIKey: true,
		Desc:     "OpenAI (GPT 系列)",
	},
	{
		Name:     "claude",
		Protocol: "anthropic",
		BaseURL:  "https://api.anthropic.com",
		Models:   []string{"claude-sonnet-4-20250514", "claude-3-5-sonnet-20241022", "claude-3-opus-20240229"},
		HasAPIKey: true,
		Desc:     "Anthropic (Claude 系列)",
	},
	{
		Name:     "deepseek",
		Protocol: "openai",
		BaseURL:  "https://api.deepseek.com/v1",
		Models:   []string{"deepseek-chat", "deepseek-coder", "deepseek-reasoner"},
		HasAPIKey: true,
		Desc:     "DeepSeek",
	},
	{
		Name:     "zhipu",
		Protocol: "openai",
		BaseURL:  "https://open.bigmodel.cn/api/paas/v4",
		Models:   []string{"glm-4", "glm-4-flash", "glm-4v"},
		HasAPIKey: true,
		Desc:     "智谱 (GLM 系列)",
	},
	{
		Name:     "qwen",
		Protocol: "openai",
		BaseURL:  "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Models:   []string{"qwen-turbo", "qwen-plus", "qwen-max", "qwen-long"},
		HasAPIKey: true,
		Desc:     "通义千问 (Qwen 系列)",
	},
	{
		Name:     "ollama",
		Protocol: "openai",
		BaseURL:  "http://localhost:11434/v1",
		Models:   []string{"llama3", "mistral", "codellama", "qwen2"},
		HasAPIKey: false,
		Desc:     "Ollama (本地模型)",
	},
	{
		Name:     "groq",
		Protocol: "openai",
		BaseURL:  "https://api.groq.com/openai/v1",
		Models:   []string{"llama3-70b-8192", "mixtral-8x7b-32768", "gemma2-9b-it"},
		HasAPIKey: true,
		Desc:     "Groq (高速推理)",
	},
	{
		Name:     "moonshot",
		Protocol: "openai",
		BaseURL:  "https://api.moonshot.cn/v1",
		Models:   []string{"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k"},
		HasAPIKey: true,
		Desc:     "月之暗面 (Kimi)",
	},
	{
		Name:     "siliconflow",
		Protocol: "openai",
		BaseURL:  "https://api.siliconflow.cn/v1",
		Models:   []string{"Qwen/Qwen2-7B-Instruct", "deepseek-ai/DeepSeek-V2-Chat", "meta-llama/Meta-Llama-3.1-8B-Instruct"},
		HasAPIKey: true,
		Desc:     "SiliconFlow (硅基流动)",
	},
}

func FindTemplate(name string) *ProviderTemplate {
	for i := range ProviderTemplates {
		if ProviderTemplates[i].Name == name {
			return &ProviderTemplates[i]
		}
	}
	return nil
}
