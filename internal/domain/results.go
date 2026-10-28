package domain

// DbQueryCount represents the SQL query counter response body.
type DbQueryCount struct {
	Count int64 `json:"dbQueryCount"`
}

// BulkWarmResult represents the bulk warm cache response body.
type BulkWarmResult struct {
	WarmedKeyCount   int   `json:"warmedKeyCount"`
	ExpiresAtEpochMs int64 `json:"expiresAtEpochMs"`
}

// CacheResetResult represents the cache reset response body.
type CacheResetResult struct {
	DeletedItemKeys int64 `json:"deletedItemKeys"`
	DeletedLockKeys int64 `json:"deletedLockKeys"`
}
