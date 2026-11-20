package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type WorkspaceItem struct {
	WorkspaceID   string   `json:"workspaceId"`
	Path          string   `json:"path"`
	Title         string   `json:"title"`
	SessionIDs    []string `json:"sessionIds"`
	SessionsKnown bool     `json:"sessionsKnown"`
	CreatedAt     string   `json:"createdAt"`
	UpdatedAt     string   `json:"updatedAt"`
}

type WorkspaceValue struct {
	Items []WorkspaceItem `json:"items"`
}

type projectRecord struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	Label        string `json:"label"`
	AddedAt      int64  `json:"addedAt"`
	LastOpenedAt int64  `json:"lastOpenedAt"`
}

var (
	workspaceValue  = WorkspaceValue{Items: []WorkspaceItem{}}
	workspaceMu     sync.RWMutex
	workspaceSubsMu sync.Mutex
	workspaceSubs   = make(map[chan struct{}]struct{})
	wakeWorkspaceCh = make(chan struct{}, 1)
)

func GetWorkspaces() WorkspaceValue {
	workspaceMu.RLock()
	defer workspaceMu.RUnlock()
	return workspaceValue
}

func SubscribeWorkspace(buf int) (<-chan struct{}, func()) {
	ch := make(chan struct{}, buf)
	workspaceSubsMu.Lock()
	workspaceSubs[ch] = struct{}{}
	workspaceSubsMu.Unlock()
	select {
	case wakeWorkspaceCh <- struct{}{}:
	default:
	}
	return ch, func() { workspaceSubsMu.Lock(); delete(workspaceSubs, ch); workspaceSubsMu.Unlock() }
}

func notifyWorkspace() {
	workspaceSubsMu.Lock()
	defer workspaceSubsMu.Unlock()
	for ch := range workspaceSubs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func timestampText(milliseconds int64) string {
	if milliseconds <= 0 {
		return ""
	}
	return time.UnixMilli(milliseconds).UTC().Format(time.RFC3339)
}

func readProjects() ([]projectRecord, error) {
	data, err := os.ReadFile(filepath.Join(globalOpenChamberHome, "settings.json"))
	if os.IsNotExist(err) {
		return []projectRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var settings struct {
		Projects []projectRecord `json:"projects"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("项目配置格式无效: %w", err)
	}
	return settings.Projects, nil
}

// readProjectSessions 读取完整分页后才确认会话数量。
func readProjectSessions(directory string) ([]string, int64, error) {
	ctx, cancel := context.WithTimeout(appContext, 10*time.Second)
	defer cancel()
	client := &http.Client{Transport: localTransport, Timeout: 5 * time.Second}
	ids := []string{}
	seenIDs, seenCursors := map[string]bool{}, map[string]bool{}
	cursor := ""
	var updated int64
	for page := 0; page < 100; page++ {
		query := url.Values{"directory": {directory}, "limit": {"100"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/session?%s", GetConfig().GetServerPort(), query.Encode()), nil)
		if err != nil {
			return nil, 0, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, 0, err
		}
		var payload struct {
			Data []struct {
				ID   string `json:"id"`
				Time struct {
					Updated  int64 `json:"updated"`
					Archived int64 `json:"archived"`
				} `json:"time"`
			} `json:"data"`
			Cursor struct {
				Next string `json:"next"`
			} `json:"cursor"`
		}
		err = json.NewDecoder(response.Body).Decode(&payload)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || err != nil || payload.Data == nil {
			return nil, 0, fmt.Errorf("会话列表不可用")
		}
		for _, session := range payload.Data {
			if session.ID == "" || session.Time.Archived > 0 || seenIDs[session.ID] {
				continue
			}
			seenIDs[session.ID] = true
			ids = append(ids, session.ID)
			if session.Time.Updated > updated {
				updated = session.Time.Updated
			}
		}
		cursor = payload.Cursor.Next
		if cursor == "" {
			return ids, updated, nil
		}
		if seenCursors[cursor] {
			return nil, 0, fmt.Errorf("会话分页游标重复")
		}
		seenCursors[cursor] = true
	}
	return nil, 0, fmt.Errorf("会话分页超出读取上限")
}

func fetchWorkspaces() error {
	if status := state.Status(); status == StatusSnapshotting || status == StatusBuilding {
		return nil
	}
	projects, err := readProjects()
	if err != nil {
		return err
	}
	value := WorkspaceValue{Items: []WorkspaceItem{}}
	seen := map[string]bool{}
	for _, project := range projects {
		if project.ID == "" || !filepath.IsAbs(project.Path) || seen[project.Path] {
			continue
		}
		seen[project.Path] = true
		item := WorkspaceItem{WorkspaceID: project.ID, Path: project.Path, Title: project.Label, SessionIDs: []string{}, CreatedAt: timestampText(project.AddedAt)}
		if item.Title == "" {
			item.Title = filepath.Base(project.Path)
		}
		if state.Status() == StatusRunning {
			if ids, updated, err := readProjectSessions(project.Path); err == nil {
				item.SessionIDs, item.SessionsKnown, item.UpdatedAt = ids, true, timestampText(updated)
			}
		}
		value.Items = append(value.Items, item)
	}
	workspaceMu.Lock()
	changed := !reflect.DeepEqual(workspaceValue, value)
	workspaceValue = value
	workspaceMu.Unlock()
	if changed {
		notifyWorkspace()
	}
	return nil
}

func StartWorkspaceWatch() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		var lastError string
		for {
			select {
			case <-appContext.Done():
				return
			case <-ticker.C:
			case <-wakeWorkspaceCh:
			}
			workspaceSubsMu.Lock()
			hasSubscribers := len(workspaceSubs) > 0
			workspaceSubsMu.Unlock()
			if !hasSubscribers {
				continue
			}
			if err := fetchWorkspaces(); err != nil {
				if err.Error() != lastError {
					LogWarning("[工作区] 读取失败: %s", err)
					lastError = err.Error()
				}
			} else {
				lastError = ""
			}
		}
	}()
}

func handleGetWorkspaces(c *gin.Context) {
	select {
	case wakeWorkspaceCh <- struct{}{}:
	default:
	}
	OK(c, GetWorkspaces())
}
