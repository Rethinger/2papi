package codex

// Image generation models offered by the ChatGPT backend. The official
// openai/codex models-manager snapshot (models.json) does not include them,
// so they live in this supplementary catalog. Enrichment fills only gaps:
// backend values always win, and the catalog only adds fields the backend
// omitted. Presence in this map also marks the model as supporting image
// generation in its capabilities payload.
var imageModelCatalog = map[string]modelCatalogEntry{
	"gpt-image-1": {
		DisplayName:     "GPT Image 1",
		Description:     "Generates and edits images from text or image prompts.",
		InputModalities: []string{"text", "image"},
	},
	"gpt-image-2": {
		DisplayName:     "GPT Image 2",
		Description:     "Generates and edits images from text or image prompts.",
		InputModalities: []string{"text", "image"},
	},
	"gpt-5.5-image": {
		DisplayName: "GPT-5.5 Image",
		Description: "Generates and edits images from text or image prompts.",
	},
	"dall-e-3": {
		DisplayName:     "DALL·E 3",
		Description:     "Generates images from text prompts.",
		InputModalities: []string{"text"},
	},
}
