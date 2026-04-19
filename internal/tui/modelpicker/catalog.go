package modelpicker

// CatalogFor returns a curated set of model ids known to be supported
// by the named provider. Hosted providers use a static list; local
// providers are typically discovered live via localdetect so the
// catalog here is a fallback for when the runner is down or hasn't
// been probed yet.
//
// Each entry carries ProviderName = the named provider so selecting
// it in the picker keeps the user on that provider. MergeLocal
// rewrites ProviderName when a local runner is reachable.
func CatalogFor(provider string) []Item {
	tag := func(items []Item) []Item {
		for i := range items {
			items[i].ProviderName = provider
		}
		return items
	}
	switch provider {
	case "anthropic":
		return tag([]Item{
			{ID: "claude-opus-4-7", Origin: "anthropic", Note: "200K ctx · flagship"},
			{ID: "claude-opus-4-6", Origin: "anthropic", Note: "200K ctx"},
			{ID: "claude-opus-4-5", Origin: "anthropic", Note: "200K ctx"},
			{ID: "claude-sonnet-4-6", Origin: "anthropic", Note: "200K ctx · fast"},
			{ID: "claude-sonnet-4-5", Origin: "anthropic", Note: "200K ctx"},
			{ID: "claude-haiku-4-5", Origin: "anthropic", Note: "200K ctx · cheapest"},
		})
	case "openai":
		return tag([]Item{
			{ID: "gpt-5", Origin: "openai", Note: "flagship"},
			{ID: "gpt-5-mini", Origin: "openai", Note: "cheaper"},
			{ID: "o3", Origin: "openai", Note: "reasoning"},
			{ID: "gpt-4.1", Origin: "openai"},
			{ID: "gpt-4o", Origin: "openai"},
			{ID: "gpt-4o-mini", Origin: "openai"},
		})
	case "google", "gemini":
		return tag([]Item{
			{ID: "gemini-2.5-pro", Origin: "google", Note: "1M ctx · flagship"},
			{ID: "gemini-2.5-flash", Origin: "google", Note: "1M ctx · fast"},
			{ID: "gemini-2.5-flash-lite", Origin: "google", Note: "cheapest"},
		})
	case "groq":
		return tag([]Item{
			{ID: "llama-3.3-70b-versatile", Origin: "groq"},
			{ID: "llama-3.1-8b-instant", Origin: "groq", Note: "fast"},
			{ID: "mixtral-8x7b-32768", Origin: "groq"},
		})
	case "deepseek":
		return tag([]Item{
			{ID: "deepseek-chat", Origin: "deepseek"},
			{ID: "deepseek-reasoner", Origin: "deepseek", Note: "R1-class"},
		})
	case "mistral":
		return tag([]Item{
			{ID: "mistral-large-latest", Origin: "mistral"},
			{ID: "mistral-small-latest", Origin: "mistral"},
			{ID: "codestral-latest", Origin: "mistral", Note: "code"},
		})
	case "xai":
		return tag([]Item{
			{ID: "grok-beta", Origin: "xai"},
		})
	case "cerebras":
		return tag([]Item{
			{ID: "llama3.3-70b", Origin: "cerebras", Note: "fastest on Cerebras"},
			{ID: "llama3.1-8b", Origin: "cerebras"},
		})
	}
	return nil
}

// MergeLocal attaches the dynamic-detected model list onto the catalog.
// Each entry gets Origin="<name> · detected" AND ProviderName=<name> so
// selecting a detected model in the picker switches the provider to
// match. Duplicates are collapsed to the detected entry (live info wins).
func MergeLocal(catalog []Item, localName string, reachable bool, models []string) []Item {
	if !reachable {
		return catalog
	}
	byID := map[string]int{}
	for i, it := range catalog {
		byID[it.ID] = i
	}
	out := append([]Item(nil), catalog...)
	for _, m := range models {
		origin := localName + " · detected"
		if idx, exists := byID[m]; exists {
			out[idx].Origin = origin
			out[idx].ProviderName = localName
			continue
		}
		out = append(out, Item{
			ID:           m,
			Origin:       origin,
			ProviderName: localName,
		})
	}
	return out
}
