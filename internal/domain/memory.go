package domain

type MemoryEntry struct {
	ID       string
	Category string // "identity", "episode", "knowledge"
	Title    string
	Content  string
	Tags     []string
	Date     string // YYYY-MM-DD
}
