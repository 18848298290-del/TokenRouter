package backup

// data_management_grpc.go defines the legacy connection and profile request
// shapes accepted by retired data-management endpoints.
// Retired data-management endpoints still decode their legacy connection and
// profile shapes before reporting service unavailability. These JSON fields keep
// that request parsing compatible, including secret-presence flags and active
// profile identifiers; the deprecated service does not apply the decoded settings.

// DataManagementPostgresConfig retains the PostgreSQL profile shape accepted by legacy configuration requests.
type DataManagementPostgresConfig struct {
	Host               string `json:"host"`                // legacy PostgreSQL server host
	Port               int32  `json:"port"`                // legacy PostgreSQL server port
	User               string `json:"user"`                // legacy PostgreSQL login name
	Password           string `json:"password,omitempty"`  // legacy PostgreSQL password value
	PasswordConfigured bool   `json:"password_configured"` // legacy password presence flag
	Database           string `json:"database"`            // legacy PostgreSQL database name
	SSLMode            string `json:"ssl_mode"`            // legacy PostgreSQL SSL mode
	ContainerName      string `json:"container_name"`      // legacy PostgreSQL container name
}

// DataManagementRedisConfig retains Redis connection fields within the retired configuration shape.
type DataManagementRedisConfig struct {
	Addr               string `json:"addr"`                // legacy Redis connection address
	Username           string `json:"username"`            // legacy Redis login name
	Password           string `json:"password,omitempty"`  // legacy Redis password value
	PasswordConfigured bool   `json:"password_configured"` // legacy password presence flag
	DB                 int32  `json:"db"`                  // legacy Redis database index
	ContainerName      string `json:"container_name"`      // legacy Redis container name
}

// DataManagementS3Config retains the object-storage profile fields of retired configuration requests.
type DataManagementS3Config struct {
	Enabled                   bool   `json:"enabled"`                      // legacy profile enabled flag
	Endpoint                  string `json:"endpoint"`                     // legacy object-storage service endpoint
	Region                    string `json:"region"`                       // legacy object-storage region label
	Bucket                    string `json:"bucket"`                       // legacy archive bucket name
	AccessKeyID               string `json:"access_key_id"`                // legacy access credential identifier
	SecretAccessKey           string `json:"secret_access_key,omitempty"`  // legacy secret credential value
	SecretAccessKeyConfigured bool   `json:"secret_access_key_configured"` // legacy secret presence flag
	Prefix                    string `json:"prefix"`                       // legacy archive key prefix
	ForcePathStyle            bool   `json:"force_path_style"`             // legacy path-style addressing flag
	UseSSL                    bool   `json:"use_ssl"`                      // legacy TLS transport flag
}

// DataManagementConfig retains profile selections and connection data for deprecated configuration binding.
type DataManagementConfig struct {
	SourceMode        string                       `json:"source_mode"`                // legacy source selection mode
	BackupRoot        string                       `json:"backup_root"`                // legacy archive root directory
	SQLitePath        string                       `json:"sqlite_path,omitempty"`      // legacy SQLite source path
	RetentionDays     int32                        `json:"retention_days"`             // legacy retention day count
	KeepLast          int32                        `json:"keep_last"`                  // legacy retained archive count
	ActivePostgresID  string                       `json:"active_postgres_profile_id"` // selected PostgreSQL profile identifier
	ActiveRedisID     string                       `json:"active_redis_profile_id"`    // selected Redis profile identifier
	Postgres          DataManagementPostgresConfig `json:"postgres"`                   // legacy PostgreSQL profile data
	Redis             DataManagementRedisConfig    `json:"redis"`                      // legacy Redis profile data
	S3                DataManagementS3Config       `json:"s3"`                         // legacy object-storage profile data
	ActiveS3ProfileID string                       `json:"active_s3_profile_id"`       // selected object-storage profile identifier
}

// DataManagementSourceConfig carries either database source shape for retired profile request binding.
type DataManagementSourceConfig struct {
	Host          string `json:"host"`               // legacy PostgreSQL server host
	Port          int32  `json:"port"`               // legacy PostgreSQL server port
	User          string `json:"user"`               // legacy PostgreSQL login name
	Password      string `json:"password,omitempty"` // legacy source password value
	Database      string `json:"database"`           // legacy PostgreSQL database name
	SSLMode       string `json:"ssl_mode"`           // legacy PostgreSQL SSL mode
	Addr          string `json:"addr"`               // legacy Redis connection address
	Username      string `json:"username"`           // legacy Redis login name
	DB            int32  `json:"db"`                 // legacy Redis database index
	ContainerName string `json:"container_name"`     // legacy source container name
}
