package tui

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/rhymeswithlimo/frost/internal/config"
)

// provider is one choice on the storage screen. S3 services are all the
// "s3" backend underneath; the presets only save typing and say where each
// value comes from.
type provider struct {
	name string
	note string // beside the name in the list
	// fields are the questions, and read/apply move answers between them
	// and the config, in field order.
	fields []field
	read   func(s config.Storage) []string
	apply  func(v []string, s *config.Storage)
	match  func(s config.Storage) bool
}

// permafrostLink is where to send someone who doesn't have a key yet.
const permafrostLink = "getfro.st/perma"

var fBucket = field{question: "What's the bucket called?", help: "Its name, exactly as you created it.", name: "bucket name", about: "bucket", check: checkBucket}

var providers = []provider{
	{
		name: "Permafrost",
		note: "recommended",
		fields: []field{
			{question: "Paste your Permafrost access key.", secret: true, help: "It stays on this machine.", name: "access key", about: "key", check: noSpaces("access key")},
		},
		read: func(s config.Storage) []string { return []string{s.Permafrost.Token} },
		// The server stays whatever it was: blank means the default one.
		apply: func(v []string, s *config.Storage) { s.Backend, s.Permafrost.Token = "permafrost", v[0] },
		match: func(s config.Storage) bool { return s.Backend == "permafrost" },
	},
	{
		name: "Backblaze B2",
		fields: []field{
			{question: "What's the bucket's endpoint?", help: "Buckets > your bucket > Endpoint. It looks like s3.us-west-004.backblazeb2.com.", name: "endpoint", about: "address", check: checkB2Endpoint},
			fBucket,
			{question: "Paste the application key's keyID.", help: "Application Keys > Add a New Application Key. Limit it to this bucket.", name: "keyID", about: "key", check: noSpaces("keyID")},
			{question: "Paste the applicationKey.", secret: true, help: "Shown once, right after you create the key.", name: "applicationKey", about: "secret", check: noSpaces("applicationKey")},
		},
		read: func(s config.Storage) []string {
			return []string{s.S3.Endpoint, s.S3.Bucket, s.S3.AccessKeyID, s.S3.SecretAccessKey}
		},
		apply: func(v []string, s *config.Storage) {
			s3(s, v[0], regionFromEndpoint(v[0]), v[1], v[2], v[3])
		},
		match: func(s config.Storage) bool {
			return s.Backend == "s3" && strings.Contains(s.S3.Endpoint, "backblazeb2.com")
		},
	},
	{
		name: "Amazon S3",
		fields: []field{
			{question: "Which region is the bucket in?", help: "Like us-east-1. The S3 console lists it next to the bucket.", name: "region", about: "address", check: checkRegion},
			fBucket,
			{question: "Paste the access key ID.", help: "IAM > Users > your user > Security credentials > Create access key.", name: "access key ID", about: "key", check: noSpaces("access key ID")},
			{question: "Paste the secret access key.", secret: true, help: "Shown once, next to the access key ID.", name: "secret access key", about: "secret", check: noSpaces("secret access key")},
		},
		read: func(s config.Storage) []string {
			return []string{s.S3.Region, s.S3.Bucket, s.S3.AccessKeyID, s.S3.SecretAccessKey}
		},
		apply: func(v []string, s *config.Storage) {
			s3(s, "s3."+v[0]+".amazonaws.com", v[0], v[1], v[2], v[3])
		},
		match: func(s config.Storage) bool {
			return s.Backend == "s3" && strings.Contains(s.S3.Endpoint, "amazonaws.com")
		},
	},
	{
		name: "Cloudflare R2",
		fields: []field{
			{question: "What's your Cloudflare account ID?", help: "On the R2 overview page, 32 letters and numbers.", name: "account ID", about: "address", check: checkAccountID},
			fBucket,
			{question: "Paste the Access Key ID.", help: "R2 > Manage R2 API Tokens > Create API token, with Object Read & Write.", name: "Access Key ID", about: "key", check: noSpaces("Access Key ID")},
			{question: "Paste the Secret Access Key.", secret: true, help: "Shown once, next to the Access Key ID.", name: "Secret Access Key", about: "secret", check: noSpaces("Secret Access Key")},
		},
		read: func(s config.Storage) []string {
			acct := strings.TrimSuffix(hostOf(s.S3.Endpoint), ".r2.cloudflarestorage.com")
			return []string{acct, s.S3.Bucket, s.S3.AccessKeyID, s.S3.SecretAccessKey}
		},
		apply: func(v []string, s *config.Storage) {
			s3(s, v[0]+".r2.cloudflarestorage.com", "auto", v[1], v[2], v[3])
		},
		match: func(s config.Storage) bool {
			return s.Backend == "s3" && strings.Contains(s.S3.Endpoint, "r2.cloudflarestorage.com")
		},
	},
	{
		name: "Wasabi",
		fields: []field{
			{question: "Which region is the bucket in?", help: "Like us-east-1 or eu-central-1. It's shown next to the bucket.", name: "region", about: "address", check: checkRegion},
			fBucket,
			{question: "Paste the access key.", help: "Access Keys > Create New Access Key.", name: "access key", about: "key", check: noSpaces("access key")},
			{question: "Paste the secret key.", secret: true, help: "Shown once, when you create the key.", name: "secret key", about: "secret", check: noSpaces("secret key")},
		},
		read: func(s config.Storage) []string {
			return []string{s.S3.Region, s.S3.Bucket, s.S3.AccessKeyID, s.S3.SecretAccessKey}
		},
		apply: func(v []string, s *config.Storage) {
			s3(s, "s3."+v[0]+".wasabisys.com", v[0], v[1], v[2], v[3])
		},
		match: func(s config.Storage) bool {
			return s.Backend == "s3" && strings.Contains(s.S3.Endpoint, "wasabisys.com")
		},
	},
	{
		name: "Other S3-compatible",
		note: "MinIO, Garage, Ceph",
		fields: []field{
			{question: "What's the S3 endpoint?", help: "Your provider's docs list it. Start with http:// only for a local test server.", name: "endpoint", about: "address", check: checkEndpoint},
			{question: "Which region?", optional: true, placeholder: "leave blank if there isn't one", help: "Only if your provider asks for one.", name: "region", about: "address", check: noSpaces("region")},
			fBucket,
			{question: "Paste the access key ID.", help: "From your provider's console.", name: "access key ID", about: "key", check: noSpaces("access key ID")},
			{question: "Paste the secret access key.", secret: true, help: "From your provider's console.", name: "secret access key", about: "secret", check: noSpaces("secret access key")},
		},
		read: func(s config.Storage) []string {
			return []string{s.S3.Endpoint, s.S3.Region, s.S3.Bucket, s.S3.AccessKeyID, s.S3.SecretAccessKey}
		},
		apply: func(v []string, s *config.Storage) { s3(s, v[0], v[1], v[2], v[3], v[4]) },
		match: func(s config.Storage) bool { return s.Backend == "s3" },
	},
}

// ---- answer checks: catch what's clearly wrong before connecting ----

func noSpaces(name string) func(string) string {
	return func(v string) string {
		if strings.ContainsAny(v, " \t") {
			return "The " + name + " has no spaces in it. Check you copied just the " + name + "."
		}
		return ""
	}
}

var (
	regionRE    = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
	accountIDRE = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
)

func checkRegion(v string) string {
	if !regionRE.MatchString(v) {
		return "Regions look like us-east-1 or eu-central-1."
	}
	return ""
}

func checkAccountID(v string) string {
	if !accountIDRE.MatchString(v) {
		return "An account ID is 32 letters and numbers. Copy it from the R2 overview page."
	}
	return ""
}

func checkBucket(v string) string {
	if len(v) < 3 || len(v) > 63 || strings.ContainsAny(v, " /\t") {
		return "Bucket names are 3 to 63 characters, with no spaces or slashes."
	}
	return ""
}

func checkEndpoint(v string) string {
	if strings.ContainsAny(v, " \t") {
		return "The endpoint has no spaces in it."
	}
	u, err := url.Parse(v)
	if err == nil && u.Host == "" {
		u, err = url.Parse("https://" + v)
	}
	if err != nil || u.Host == "" || strings.Trim(u.Path, "/") != "" {
		return "Type just the host name, like s3.example.com."
	}
	return ""
}

func checkB2Endpoint(v string) string {
	if msg := checkEndpoint(v); msg != "" {
		return msg
	}
	if !strings.HasSuffix(strings.TrimSuffix(hostOf(v), "/"), ".backblazeb2.com") {
		return "Backblaze endpoints end in backblazeb2.com, like s3.us-west-004.backblazeb2.com."
	}
	return ""
}

// s3 fills in an S3 config. The folder inside the bucket isn't asked for,
// so an existing one is kept.
func s3(s *config.Storage, endpoint, region, bucket, id, secret string) {
	s.Backend = "s3"
	s.S3.Endpoint, s.S3.Region, s.S3.Bucket = strings.TrimSuffix(endpoint, "/"), region, bucket
	s.S3.AccessKeyID, s.S3.SecretAccessKey = id, secret
}

// matchProvider picks the provider for an existing config. The first match
// wins, so the catch-all S3 entry goes last.
func matchProvider(s config.Storage) int {
	for i, p := range providers {
		if p.match(s) {
			return i
		}
	}
	return 0
}

// hostOf strips an optional scheme from an endpoint.
func hostOf(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return endpoint
}

// regionFromEndpoint reads the region out of s3.<region>.example.com.
func regionFromEndpoint(endpoint string) string {
	parts := strings.Split(hostOf(endpoint), ".")
	if len(parts) >= 3 && parts[0] == "s3" {
		return parts[1]
	}
	return ""
}

// describeStorage is a one-line summary for the review screen.
func describeStorage(s config.Storage) string {
	p := providers[matchProvider(s)]
	if s.Backend == "permafrost" {
		if s.Permafrost.URL != "" {
			return p.name + ", " + hostOf(s.Permafrost.URL)
		}
		return p.name
	}
	where := s.S3.Bucket
	if s.S3.Prefix != "" {
		where += "/" + strings.Trim(s.S3.Prefix, "/")
	}
	return p.name + ", bucket " + where
}
