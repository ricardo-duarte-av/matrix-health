package main

import (
        "context"
        "encoding/json"
        "fmt"
        "io/ioutil"
        "net"
        "net/http"
        "os"
        "strings"
        "time"
        "sync"

        "gopkg.in/yaml.v3"
        "maunium.net/go/mautrix"
        "maunium.net/go/mautrix/event"
        "maunium.net/go/mautrix/id"
)

// Config represents the structure of the YAML configuration file
type Config struct {
        ServerName string `yaml:"servername"`
        Username   string `yaml:"username"`
        Password   string `yaml:"password"`
        LogRoom    string `yaml:"logroom"`
        Interval   int    `yaml:"interval"` // Interval in seconds
}

var config Config

func main() {
        fmt.Println("Starting Matrix client...")

        // Load the configuration
        err := loadConfig("config.yaml")
        if err != nil {
                fmt.Println("Failed to load configuration:", err)
                return
        }

        fmt.Println("Configuration loaded successfully.")
        fmt.Printf("ServerName: %s, Username: %s, LogRoom: %s, Interval: %d seconds\n",
                config.ServerName, config.Username, config.LogRoom, config.Interval)

        // Validate username format
        fmt.Println("Validating username format...")
        if _, _, err := id.UserID(config.Username).ParseAndValidate(); err != nil {
                fmt.Println("Invalid username in configuration:", err)
                return
        }
        fmt.Println("Username is valid.")

        // Create a new Matrix client
        fmt.Println("Creating Matrix client...")
        client, err := mautrix.NewClient(config.ServerName, "", "")
        if err != nil {
                fmt.Println("Failed to create Matrix client:", err)
                return
        }
        fmt.Println("Matrix client created.")

        // Log in to the Matrix account
        fmt.Println("Logging in...")
        ctx := context.Background()
        loginResp, err := client.Login(ctx, &mautrix.ReqLogin{
                Type: mautrix.AuthTypePassword,
                Identifier: mautrix.UserIdentifier{
                        Type: mautrix.IdentifierTypeUser,
                        User: config.Username,
                },
                Password: config.Password,
        })
        if err != nil {
                fmt.Println("Failed to log in:", err)
                return
        }

        // Set the access token explicitly
        client.AccessToken = loginResp.AccessToken
        fmt.Printf("Logged in successfully as %s\n", config.Username)

        // Use WaitGroup to run the HTTP server and the health checker concurrently
        var wg sync.WaitGroup
        wg.Add(2)

        // Start the server check loop
        go func() {
                defer wg.Done()
                runServerCheckLoop(ctx, client)
        }()

        // Start the HTTP server for visualization
        go func() {
                defer wg.Done()
                basePath, err := os.Getwd()
                if err != nil {
                        fmt.Println("Failed to get working directory:", err)
                        return
                }
                StartHTTPServer(client, basePath) // Start the HTTP server
        }()

        // Wait for both goroutines to finish
        wg.Wait()
}


// resolveMatrixServer resolves the actual Matrix server URL using .well-known, DNS SRV, or fallback to server-name.com:8448
func resolveMatrixServer(server string) (string, error) {
        // 1. Check if the server is an IP literal
        if net.ParseIP(server) != nil {
                // If server is an IP literal, return it with port 8448 (default Matrix port)
                return fmt.Sprintf("%s:8448", server), nil
        }

        // 2. Try .well-known delegation
        wellKnownURL := fmt.Sprintf("https://%s/.well-known/matrix/server", server)
        client := &http.Client{
                Timeout: 5 * time.Second,
        }
        resp, err := client.Get(wellKnownURL)
        if err == nil {
                defer resp.Body.Close()
                if resp.StatusCode == http.StatusOK {
                        var result struct {
                                Server string `json:"m.server"`
                        }
                        err = json.NewDecoder(resp.Body).Decode(&result)
                        if err == nil && result.Server != "" {
                                // Parse the m.server result
                                parts := strings.Split(result.Server, ":")
                                if len(parts) == 2 {
                                        return result.Server, nil
                                }
                                return fmt.Sprintf("%s:8448", result.Server), nil
                        }
                }
        }

        // 3. Look for SRV record `_matrix-fed._tcp.<hostname>` (newer)
        _, srvRecords, err := net.LookupSRV("matrix-fed", "tcp", server)
        if err == nil && len(srvRecords) > 0 {
                srv := srvRecords[0] // Use the first SRV record
                return fmt.Sprintf("%s:%d", strings.Trim(srv.Target, "."), srv.Port), nil
        }

        // 4. Look for SRV record `_matrix._tcp.<hostname>` (deprecated)
        _, srvRecordsDeprecated, err := net.LookupSRV("matrix", "tcp", server)
        if err == nil && len(srvRecordsDeprecated) > 0 {
                srv := srvRecordsDeprecated[0] // Use the first SRV record
                return fmt.Sprintf("%s:%d", strings.Trim(srv.Target, "."), srv.Port), nil
        }

        // 5. Fallback to server-name.com:8448
        _, addrsErr := net.LookupHost(server)
        if addrsErr == nil {
                return fmt.Sprintf("%s:8448", server), nil
        }

        return "", fmt.Errorf("could not resolve Matrix server for %s", server)
}


// Shared map to store the tree structure (rooms and servers)
var treeData sync.Map

// runServerCheckLoop performs checks for offline servers at the specified interval
func runServerCheckLoop(ctx context.Context, client *mautrix.Client) {
        for {
                fmt.Println("Checking server statuses...")

                // Get all joined rooms
                joinedRooms, err := client.JoinedRooms(ctx)
                if err != nil {
                        fmt.Println("Failed to fetch joined rooms:", err)
                        time.Sleep(time.Duration(config.Interval) * time.Second)
                        continue
                }

                // Create a WaitGroup for room-level parallelism
                var roomWg sync.WaitGroup

                // Protect shared logs and sendMessageToRoom calls from concurrent writes
                var logMutex sync.Mutex

                // Process each room in parallel
                for _, roomID := range joinedRooms.JoinedRooms {
                        roomWg.Add(1) // Increment the counter for room-level WaitGroup

                        go func(roomID string) {
                                defer roomWg.Done() // Decrement the counter when the room goroutine finishes

                                // Skip the log room
                                if id.RoomID(roomID) == id.RoomID(config.LogRoom) {
                                        logMutex.Lock()
                                        fmt.Printf("Skipping log room: %s\n", config.LogRoom)
                                        logMutex.Unlock()
                                        return
                                }

                                // Log the room being tested
                                logMutex.Lock()
                                fmt.Printf("Processing room: %s\n", roomID)
                                logMutex.Unlock()

                                // Fetch members of the room
                                resp, err := client.JoinedMembers(ctx, id.RoomID(roomID))
                                if err != nil {
                                        logMutex.Lock()
                                        fmt.Printf("Failed to get joined members for room %s: %v\n", roomID, err)
                                        logMutex.Unlock()
                                        return
                                }

                                // Fetch or create a room node in the tree
                                roomNode, ok := getOrCreateRoomNode(ctx, client, roomID)
                                if !ok {
                                        logMutex.Lock()
                                        fmt.Printf("Failed to create or retrieve room node for %s\n", roomID)
                                        logMutex.Unlock()
                                        return
                                }

                                // Deduplicate servers for this room
                                uniqueServers := make(map[string]struct{})
                                for userID := range resp.Joined {
                                        server := extractDomain(string(userID)) // Extract the domain of the user ID
                                        uniqueServers[server] = struct{}{}     // Add the server to the map
                                }

                                // Create a WaitGroup for server-level parallelism
                                var serverWg sync.WaitGroup

                                // Check each unique server in parallel
                                for server := range uniqueServers {
                                        // Fetch or create a server node in the room
                                        serverNode := getOrCreateServerNode(roomNode, server)

                                        serverWg.Add(1) // Increment the counter for server-level WaitGroup

                                        go func(server string, serverNode *TreeNode) {
                                                defer serverWg.Done() // Decrement the counter when the server goroutine finishes

                                                // Check the server status
                                                status := checkServer(ctx, client, server)

                                                // Debug: Log server and status
                                                logMutex.Lock()
                                                fmt.Printf("Server %s in room %s: Before updating, Status: %s\n", server, roomID, serverNode.Status)
                                                logMutex.Unlock()

                                                // Update the server status
                                                serverNode.Status = status

                                                logMutex.Lock()
                                                fmt.Printf("Server %s in room %s: After updating, Status: %s\n", server, roomID, serverNode.Status)
                                                logMutex.Unlock()
                                        }(server, serverNode)
                                }

                                // Wait for all server checks in the room to complete
                                serverWg.Wait()
                        }(string(roomID)) // Convert roomID (id.RoomID) to string
                }

                // Wait for all room checks to complete
                roomWg.Wait()

                // Wait for the specified interval before checking again
                fmt.Printf("Waiting for %d seconds\n", config.Interval)
                time.Sleep(time.Duration(config.Interval) * time.Second)
        }
}



// getOrCreateRoomNode fetches or creates a room node in the tree
func getOrCreateRoomNode(ctx context.Context, client *mautrix.Client, roomID string) (*TreeNode, bool) {
    // Fetch the room node if it exists
    if node, ok := treeData.Load(roomID); ok {
        return node.(*TreeNode), true
    }

    // Fetch room details (title and alias)
    roomAlias, roomTitle := getRoomDetails(ctx, client, id.RoomID(roomID))

    // Ensure the room alias starts with a single #
    if !strings.HasPrefix(roomAlias, "#") {
        roomAlias = "#" + roomAlias
    }

    // Format the name as "Room Title - Room Alias"
    formattedName := fmt.Sprintf("%s - %s", roomTitle, roomAlias)

    // Create a new room node
    roomNode := &TreeNode{
        Name:     formattedName, // Use the formatted name for the room
        Status:   "ok",          // Default room status
        Children: []*TreeNode{},
    }

    // Store the new room node in the treeData
    treeData.Store(roomID, roomNode)
    return roomNode, true
}


// getOrCreateServerNode fetches or creates a server node in a room
func getOrCreateServerNode(roomNode *TreeNode, server string) *TreeNode {
        // Check if the server already exists in the room
        for _, child := range roomNode.Children {
                if child.Name == server {
                        return child
                }
        }

        // Create a new server node with default status "unknown"
        serverNode := &TreeNode{
                Name:   server,
                Status: "unknown",
        }

        // Add the new server node to the room
        roomNode.Children = append(roomNode.Children, serverNode)
        return serverNode
}



const CanonicalAliasEventType = "m.room.canonical_alias" // Define the event type as a string

// getRoomDetails fetches the main alias and title of a room
func getRoomDetails(ctx context.Context, client *mautrix.Client, roomID id.RoomID) (string, string) {
        // Fetch the room name (title)
        var roomName struct {
                Name string `json:"name"`
        }
        err := client.StateEvent(ctx, roomID, event.StateRoomName, "", &roomName)
        if err != nil || roomName.Name == "" {
                roomName.Name = "(unknown title)"
        }

        // Fetch the canonical alias
        canonicalAliasType := event.NewEventType("m.room.canonical_alias") // Create the type for m.room.canonical_alias
        var canonicalAlias struct {
                Alias string `json:"alias"`
        }
        err = client.StateEvent(ctx, roomID, canonicalAliasType, "", &canonicalAlias)
        if err != nil || canonicalAlias.Alias == "" {
                fmt.Printf("No canonical alias found for room %s\n", roomID)
                return roomID.String(), roomName.Name // Use Room ID as fallback for alias
        }

        // Use the canonical alias as the main alias
        return canonicalAlias.Alias, roomName.Name
}




// checkServer resolves and checks the online status of a server
func checkServer(ctx context.Context, client *mautrix.Client, server string) string {
        matrixServer, err := resolveMatrixServer(server)
        if err != nil {
                return fmt.Sprintf("Failed (Delegation Failed: %v)", err)
        }

        if checkServerOnline(matrixServer) {
                return "OK"
        }
        return "Failed (Unreachable)"
}

// extractDomain extracts the domain part of a Matrix UserID
func extractDomain(userID string) string {
        parts := strings.Split(userID, ":")
        if len(parts) > 1 {
                return parts[1] // Return the domain part after ":"
        }
        return ""
}

// checkServerOnline checks if a server is online by sending a GET request to the Matrix federation version endpoint
func checkServerOnline(server string) bool {
        url := fmt.Sprintf("https://%s/_matrix/federation/v1/version", server)
        client := &http.Client{
                Timeout: 5 * time.Second,
        }
        resp, err := client.Get(url)
        if err != nil {
                fmt.Printf("Failed to reach server %s: %v\n", server, err)
                return false
        }
        defer resp.Body.Close()

        // Check if the response is valid JSON
        var result map[string]interface{}
        err = json.NewDecoder(resp.Body).Decode(&result)
        if err != nil {
                fmt.Printf("Invalid JSON response from server %s: %v\n", server, err)
                return false
        }
        return true
}

// sendMessageToRoom sends a message to a Matrix room
func sendMessageToRoom(ctx context.Context, client *mautrix.Client, roomID id.RoomID, message string) error {
        _, err := client.SendText(ctx, roomID, message)
        return err
}

func loadConfig(path string) error {
        fmt.Printf("Loading configuration from: %s\n", path)
        data, err := ioutil.ReadFile(path)
        if err != nil {
                return err
        }
        return yaml.Unmarshal(data, &config)
}



daedric@ns3167742:~/matrix-health-dev$ ^C
daedric@ns3167742:~/matrix-health-dev$ cat webpage.go
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
