package main

import (
        "context"
        "encoding/json"
        "fmt"
        "net/http"
        "path/filepath"
        "sync"

        "maunium.net/go/mautrix"
        "maunium.net/go/mautrix/id"
        "maunium.net/go/mautrix/event"
)


// HTTP server integration for serving the /tree endpoint and index.html
// ==============================================================

// TreeNode represents a node in the tree structure for D3.js
type TreeNode struct {
    Name     string      `json:"name"`
    Avatar   string      `json:"avatar,omitempty"`
    Status   string      `json:"status,omitempty"` // Add Status field for server status
    UserCount int        `json:"user_count,omitempty"` // Number of users from this server in this room
    Children []*TreeNode `json:"children,omitempty"`
}

// A shared map to store the statuses of servers. This is updated in runServerCheckLoop.
var serverStatuses sync.Map

// ServerTreeHandler generates the JSON response for the tree visualization.
func ServerTreeHandler(w http.ResponseWriter, r *http.Request) {
        // Create a root node for the tree
        root := &TreeNode{
                Name:     "Root",
                Status:   "ok",
                Children: []*TreeNode{},
        }

        // Iterate over all rooms in the shared treeData map
        treeData.Range(func(key, value interface{}) bool {
                // Append each room node to the root's children
                roomNode, ok := value.(*TreeNode)
                if ok {
                        root.Children = append(root.Children, roomNode)
                }
                return true
        })

        // Write the tree as JSON response
        w.Header().Set("Content-Type", "application/json")
        if err := json.NewEncoder(w).Encode(root); err != nil {
                http.Error(w, "Failed to encode tree data", http.StatusInternalServerError)
        }
}


// ServeIndexHandler serves the D3.js visualization HTML file
func ServeIndexHandler(basePath string) http.HandlerFunc {
        return func(w http.ResponseWriter, r *http.Request) {
                // Serve the index.html file
                http.ServeFile(w, r, filepath.Join(basePath, "index.html"))
        }
}

// StartHTTPServer starts an HTTP server to serve the /tree JSON endpoint and the D3.js visualization
func StartHTTPServer(client *mautrix.Client, basePath string) {
        http.HandleFunc("/tree", ServerTreeHandler)
        http.HandleFunc("/", ServeIndexHandler(basePath)) // Serve the index.html on the root path

        fmt.Println("HTTP server running at http://localhost:6000")
        http.ListenAndServe("0.0.0.0:6000", nil)
}


// FetchAvatarURL fetches the avatar URL for a given user or room
func FetchAvatarURL(ctx context.Context, client *mautrix.Client, roomID id.RoomID, userID id.UserID) string {
        fmt.Printf("FetchAvatarURL called with roomID: %s, userID: %s\n", roomID, userID)
        // Helper function to construct the full URL for MXC URIs
        buildFullAvatarURL := func(contentURI id.ContentURI) string {
                if contentURI.IsEmpty() {
                        return ""
                }
                return fmt.Sprintf("%s/_matrix/media/v3/download/%s/%s", client.HomeserverURL, contentURI.Homeserver, contentURI.FileID)
        }

        // Fetch for user avatar
        if userID != "" {
                fmt.Printf("Fetching User Avatar URL: %s\n", userID)
                profile, err := client.GetProfile(ctx, userID)
                if err == nil && !profile.AvatarURL.IsEmpty() {
                        fmt.Printf("User Avatar URL: %s\n", profile.AvatarURL)
                        return buildFullAvatarURL(profile.AvatarURL)
                }
                // Generate a placeholder if no avatar is found
                username := string(userID)
                if len(username) > 0 {
                        firstLetter := string(username[1]) // Skip the `@` in the username
                        return fmt.Sprintf("https://dummyimage.com/24x24/0074D9/FFFFFF.png&text=%s", firstLetter)
                }
                return "https://dummyimage.com/24x24/0074D9/FFFFFF.png&text=B" // Default bot avatar
        }

        // Fetch for room avatar
        if roomID != "" {
                fmt.Printf("Fetching Room Avatar URL: %s\n", roomID)
                var roomAvatar struct {
                        AvatarURL id.ContentURI `json:"url"`
                }
                err := client.StateEvent(ctx, roomID, event.StateRoomAvatar, "", &roomAvatar)
                if err == nil && !roomAvatar.AvatarURL.IsEmpty() {
                        fmt.Printf("Room Avatar URL: %s\n", roomAvatar.AvatarURL)
                        return buildFullAvatarURL(roomAvatar.AvatarURL)
                }
                // Generate a placeholder if no avatar is found
                roomName := string(roomID)
                if len(roomName) > 0 {
                        firstLetter := string(roomName[1]) // Skip the `!` in the room ID
                        return fmt.Sprintf("https://dummyimage.com/24x24/FF4136/FFFFFF.png&text=%s", firstLetter)
                }
                return "https://dummyimage.com/24x24/FF4136/FFFFFF.png&text=R" // Default room avatar
        }

        return ""
}
