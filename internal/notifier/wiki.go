// Package notifier 提供向 wiki-api 通知新 repo 的能力。
//
// 当 trending 数据爬取并落库后，异步通知 wiki-api 预探测，
// 使 Starcat 客户端查询时可直接命中缓存，避免冷启动三次网络探测。
//
// 启用方式：设置环境变量 WIKI_API_URL 和 WIKI_API_KEY。
// 不设置则静默跳过，保持向后兼容。
package notifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const batchSize = 50

// WikiNotifier 向 wiki-api 发送批量探测请求。
type WikiNotifier struct {
	apiURL string
	apiKey string
	client *http.Client
}

// NewWikiNotifier 创建 WikiNotifier。
// 若 WIKI_API_URL 或 WIKI_API_KEY 任一为空，返回 nil（功能禁用）。
func NewWikiNotifier() *WikiNotifier {
	apiURL := os.Getenv("WIKI_API_URL")
	if apiURL == "" {
		apiURL = "http://127.0.0.1:5004"
	}
	apiKey := os.Getenv("WIKI_API_KEY")
	if apiKey == "" {
		log.Println("[notifier] WIKI_API_KEY 未设置，wiki 预热已禁用")
		return nil
	}

	return &WikiNotifier{
		apiURL: apiURL,
		apiKey: apiKey,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// NotifyRepos 异步通知 wiki-api 探测指定 repo 列表。
// 内部按 50 个一批分割，fire-and-forget，不阻塞主流程。
func (n *WikiNotifier) NotifyRepos(fullNames []string) {
	if n == nil || len(fullNames) == 0 {
		return
	}

	// 清洗前导/后置斜杠
	cleaned := make([]string, 0, len(fullNames))
	for _, fn := range fullNames {
		fn = strings.Trim(fn, "/")
		if fn != "" && strings.Count(fn, "/") >= 1 {
			cleaned = append(cleaned, fn)
		}
	}
	if len(cleaned) == 0 {
		return
	}

	// 异步发送，不阻塞主流程
	go n.batchNotify(cleaned)
}

// batchNotify 分批发送探测请求。
func (n *WikiNotifier) batchNotify(fullNames []string) {
	for i := 0; i < len(fullNames); i += batchSize {
		end := i + batchSize
		if end > len(fullNames) {
			end = len(fullNames)
		}
		batch := fullNames[i:end]

		if err := n.sendBatch(batch); err != nil {
			log.Printf("[notifier] wiki batch (%d repos) failed: %v", len(batch), err)
			// 失败不重试，下次 cron 会再触发
			continue
		}

		log.Printf("[notifier] wiki batch (%d repos) ok", len(batch))

		// 避免打爆 wiki-api
		time.Sleep(500 * time.Millisecond)
	}
}

// sendBatch 发送单批探测请求。
func (n *WikiNotifier) sendBatch(fullNames []string) error {
	body := struct {
		Repos []string `json:"repos"`
	}{Repos: fullNames}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest(
		"POST",
		n.apiURL+"/api/v1/wikis/batch",
		bytes.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+n.apiKey)
	// 聚合网关（starcat-api）按 X-SC-Svc 分流；独立 wiki-api 忽略未知头，可安全始终带上。
	req.Header.Set("X-SC-Svc", "wiki")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}

	return nil
}

// IsEnabled 返回是否已启用 wiki 预热。
func (n *WikiNotifier) IsEnabled() bool {
	return n != nil
}
