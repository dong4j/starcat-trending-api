package handler

import (
	"net/http"
	"time"

	"github.com/starcat-app/starcat-trending-api/internal/model"
	"github.com/starcat-app/starcat-trending-api/internal/spider"
)

// HandleUsersV1 GET /api/v1/users - 返回 trending 开发者列表。
func HandleUsersV1() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := r.URL.Query().Get("lang")
		since := r.URL.Query().Get("since")
		if since == "" {
			since = "daily"
		}
		sponsorable := r.URL.Query().Get("sponsorable")

		sp := spider.NewUserSpider(since, lang, sponsorable)
		items := sp.GetItems()

		developers := make([]model.Developer, len(items))
		for i, item := range items {
			d := model.Developer{
				Avatar:     item.Avatar,
				Name:       item.Name,
				GitHubName: item.GitHubName,
			}
			if item.Popular != nil {
				d.Popular = &struct {
					Repo string `json:"repo"`
					Desc string `json:"desc"`
				}{Repo: item.Popular.Repo, Desc: item.Popular.Desc}
			}
			developers[i] = d
		}

		writeJSONWithMeta(w, developers, &model.Meta{
			Since:       since,
			Language:    lang,
			Total:       len(developers),
			GeneratedAt: time.Now().Format(time.RFC3339),
		})
	}
}
