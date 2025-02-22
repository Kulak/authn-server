package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/keratin/authn-server/app/data/private"

	// a .env file is extremely useful during development
	_ "github.com/joho/godotenv/autoload"
	"github.com/keratin/authn-server/lib/oauth"
	"github.com/keratin/authn-server/lib/route"
	"github.com/keratin/authn-server/ops"
	"golang.org/x/crypto/pbkdf2"
)

// Config is the full list of configuration settings for AuthN. It is typically populated by reading
// environment variables.
type Config struct {
	AppPasswordlessTokenURL     *url.URL
	PasswordlessTokenTTL        time.Duration
	PasswordlessTokenSigningKey []byte
	AppPasswordResetURL         *url.URL
	AppPasswordChangedURL       *url.URL
	ApplicationDomains          []route.Domain
	BcryptCost                  int
	UsernameIsEmail             bool
	UsernameMinLength           int
	UsernameDomains             []string
	PasswordMinComplexity       int
	RefreshTokenTTL             time.Duration
	RedisURL                    *url.URL
	DatabaseURL                 *url.URL
	SessionCookieName           string
	OAuthCookieName             string
	SessionSigningKey           []byte
	ResetSigningKey             []byte
	DBEncryptionKey             []byte
	OAuthSigningKey             []byte
	ResetTokenTTL               time.Duration
	IdentitySigningKey          *private.Key
	AuthNURL                    *url.URL
	ForceSSL                    bool
	MountedPath                 string
	AccessTokenTTL              time.Duration
	AuthUsername                string
	AuthPassword                string
	EnableSignup                bool
	StatisticsTimeZone          *time.Location
	DailyActivesRetention       int
	WeeklyActivesRetention      int
	ErrorReporterCredentials    string
	ErrorReporterType           ops.ErrorReporterType
	ServerPort                  int
	PublicPort                  int
	Proxied                     bool
	GoogleOauthCredentials      *oauth.Credentials
	GitHubOauthCredentials      *oauth.Credentials
	FacebookOauthCredentials    *oauth.Credentials
	DiscordOauthCredentials     *oauth.Credentials
}

var configurers = []configurer{
	// The APP_DOMAINS are a list of domains that may refer traffic and be valid JWT audiences. If
	// the domain includes a port, it must match referred traffic. If the domain does not include a
	// port, it will match any referred traffic port. Ports 80 and 443 are matched against schemes.
	func(c *Config) error {
		val, err := requireEnv("APP_DOMAINS")
		if err == nil {
			c.ApplicationDomains = make([]route.Domain, 0)
			for _, domain := range strings.Split(val, ",") {
				c.ApplicationDomains = append(c.ApplicationDomains, route.ParseDomain(domain))
			}
		}
		return err
	},

	// The AUTHN_URL is used as an issuer for ID tokens, and must be a URL that
	// the application can resolve in order to fetch our public key for JWT
	// verification.
	//
	// If the AUTHN_URL includes a path, all API routes will be relative to it.
	//
	// example: https://app.domain.com/authn
	func(c *Config) error {
		val, err := lookupURL("AUTHN_URL")
		if err == nil {
			if val == nil {
				return ErrMissingEnvVar("AUTHN_URL")
			}
			c.AuthNURL = val
			if val.Path == "" {
				c.MountedPath = "/"
			} else {
				c.MountedPath = val.Path
			}
			c.ForceSSL = val.Scheme == "https"
		}
		return err
	},

	// The SECRET_KEY_BASE is a random seed that AuthN can use to derive keys for
	// other purposes, like HMAC signing of JWT sessions with the AuthN server.
	// The key is not used directly, but is passed through an expensive derivation
	// that means any attempt to brute-force the base secret from a signature will
	// have a high work factor in addition to a large search space.
	//
	// This does not protect the derived key from being brute-forced, of course.
	// But it does help in case the key base has less entropy than might be ideal,
	// and it does protect from escalating an attack on one derived key into an
	// attack on all of the derived keys.
	func(c *Config) error {
		fileName, err := requireEnv("SECRET_KEY_BASE_FILE")
		if err != nil {
			fmt.Printf("Failed to load SECRET_KEY_BASE_FILE: %v", err)
			val, err := requireEnv("SECRET_KEY_BASE")
			if err != nil {
				return err
			}
			c.SessionSigningKey = derive([]byte(val), "session-key-salt")
			c.ResetSigningKey = derive([]byte(val), "password-reset-token-key-salt")
			c.PasswordlessTokenSigningKey = derive([]byte(val), "passwordless-token-key-salt")
			c.DBEncryptionKey = derive([]byte(val), "db-encryption-key-salt")[:32]
			c.OAuthSigningKey = derive([]byte(val), "oauth-key-salt")
			return nil
		}
		val, err := ioutil.ReadFile(fileName)
		if err != nil {
			return err
		}
		c.SessionSigningKey = derive(val, "session-key-salt")
		c.ResetSigningKey = derive(val, "password-reset-token-key-salt")
		c.PasswordlessTokenSigningKey = derive(val, "passwordless-token-key-salt")
		c.DBEncryptionKey = derive(val, "db-encryption-key-salt")[:32]
		c.OAuthSigningKey = derive(val, "oauth-key-salt")
		return nil
	},

	// BCRYPT_COST describes how many times a password should be hashed. Costs are
	// exponential, and may be increased later without waiting for a user to return
	// and log in.
	//
	// The ideal cost is the slowest one that can be performed without a slow login
	// experience and without creating CPU bottlenecks or easy DDOS attack vectors.
	//
	// There's no reason to go below 10, and 12 starts to become noticeable on
	// current hardware.
	func(c *Config) error {
		cost, err := lookupInt("BCRYPT_COST", 11)
		if err == nil {
			if cost < 10 {
				return fmt.Errorf("BCRYPT_COST is too low: %v", cost)
			}
			c.BcryptCost = cost
		}
		return err
	},

	// PASSWORD_POLICY_SCORE is a minimum complexity score that a password must get
	// from the zxcvbn algorithm, where:
	//
	// * 0 - too guessable
	// * 1 - very guessable
	// * 2 - somewhat guessable (default)
	// * 3 - safely unguessable
	// * 4 - very unguessable
	//
	// See: see: https://blogs.dropbox.com/tech/2012/04/zxcvbn-realistic-password-strength-estimation/
	func(c *Config) error {
		minScore, err := lookupInt("PASSWORD_POLICY_SCORE", 2)
		if err == nil {
			c.PasswordMinComplexity = minScore
		}
		return err
	},

	// A DATABASE_URL is a string that can specify the database engine, connection
	// details, credentials, and other details.
	//
	// Example: sqlite3://localhost/authn-go
	func(c *Config) error {
		val, err := lookupSecureURL("DATABASE_URL_FILE")
		if err != nil {
			// fallback
			fmt.Printf("DATABASE_URL_FILE error, falling back to DATABASE_URL: %v\n", err)
			val, err := lookupURL("DATABASE_URL")
			if err == nil {
				if val == nil {
					return ErrMissingEnvVar("DATABASE_URL")
				}
				c.DatabaseURL = val
				return nil
			}
		}
		c.DatabaseURL = val
		return nil
	},

	// REDIS_URL is a string format that can specify any option for connecting to
	// a Redis server.
	//
	// Example: redis://127.0.0.1:6379/11
	func(c *Config) error {
		val, err := lookupURL("REDIS_URL")
		if err == nil {
			c.RedisURL = val
		}
		return err
	},

	// USERNAME_IS_EMAIL is a truthy string ("t", "true", "yes") that enables the
	// email validations for username fields. By default, usernames are just
	// strings.
	func(c *Config) error {
		isEmail, err := lookupBool("USERNAME_IS_EMAIL", false)
		if err == nil {
			c.UsernameIsEmail = isEmail
		}
		return err
	},

	// ENABLE_SIGNUP may be set to a falsy value ("f", "false", "no") to disable
	// signup endpoints.
	func(c *Config) error {
		enableSignup, err := lookupBool("ENABLE_SIGNUP", true)
		if err == nil {
			c.EnableSignup = enableSignup
		}
		return err
	},

	// EMAIL_USERNAME_DOMAINS is a comma-delimited list of domains that an email
	// username must contain for signup. If missing, then any domain is a valid
	// signup.
	//
	// This setting only has effect if USERNAME_IS_EMAIL has been set.
	func(c *Config) error {
		if val, ok := os.LookupEnv("EMAIL_USERNAME_DOMAINS"); ok {
			c.UsernameDomains = strings.Split(val, ",")
		}
		return nil
	},

	// REFRESH_TOKEN_TTL determines how long a refresh token will live after its
	// last touch. This is necessary to prevent years-long Redis bloat from
	// inactive sessions, where users close the window rather than log out.
	func(c *Config) error {
		ttl, err := lookupInt("REFRESH_TOKEN_TTL", 86400*30)
		if err == nil {
			c.RefreshTokenTTL = time.Duration(ttl) * time.Second
		}
		return err
	},

	// PASSWORD_RESET_TOKEN_TTL determines how long a password reset token (as JWT)
	// will be valid from when it is generated. These tokens should not live much
	// longer than it takes for an attentive user to act in a reasonably expedient
	// manner. If a user loses control of a password reset token, they will lose
	// control of their account.
	func(c *Config) error {
		ttl, err := lookupInt("PASSWORD_RESET_TOKEN_TTL", 1800)
		if err == nil {
			c.ResetTokenTTL = time.Duration(ttl) * time.Second
		}
		return err
	},

	// PASSWORDLESS_TOKEN_TTL determines how long a passwordless token (as JWT)
	// will be valid from when it is generated. These tokens should not live much
	// longer than it takes for an attentive user to act in a reasonably expedient
	// manner. If a user loses control of a passwordless token, they will lose
	// control of their account.
	func(c *Config) error {
		ttl, err := lookupInt("PASSWORDLESS_TOKEN_TTL", 1800)
		if err == nil {
			c.PasswordlessTokenTTL = time.Duration(ttl) * time.Second
		}
		return err
	},

	// ACCESS_TOKEN_TTL determines how long an access token (as JWT) will remain
	// valid. This is a hard limit, to limit the potential damage of an exposed
	// access token.
	//
	// New access tokens can be generated using the refresh token for as long as
	// the refresh token remains valid. This is an important mechanism because it
	// allows the authentication server to revoke sessions (e.g. on logout) with
	// confidence that any related access tokens will expire soon.
	//
	// Note that revoking a refresh token will not invalidate access tokens until
	// this TTL passes, so shorter is better.
	func(c *Config) error {
		ttl, err := lookupInt("ACCESS_TOKEN_TTL", 3600)
		if err == nil {
			c.AccessTokenTTL = time.Duration(ttl) * time.Second
		}
		return err
	},

	// HTTP_AUTH_USERNAME and HTTP_AUTH_PASSWORD specify the basic auth credentials
	// that must be provided to access private endpoints.
	//
	// This security pattern requires communication with AuthN to use SSL.
	func(c *Config) error {
		if val, ok := os.LookupEnv("HTTP_AUTH_USERNAME"); ok {
			c.AuthUsername = val
		} else {
			i, err := rand.Int(rand.Reader, big.NewInt(99999999))
			if err != nil {
				return err
			}
			c.AuthUsername = i.String()
		}
		if val, ok := os.LookupEnv("HTTP_AUTH_PASSWORD"); ok {
			c.AuthPassword = val
		} else {
			i, err := rand.Int(rand.Reader, big.NewInt(99999999))
			if err != nil {
				return err
			}
			c.AuthPassword = i.String()
		}
		return nil
	},

	// APP_PASSWORD_CHANGED_URL is an endpoint that will be notified when an account
	// has changed its password. This notification may be used to deliver an email
	// confirmation.
	//
	// For security, this URL should specify https and include a basic auth username
	// and password.
	func(c *Config) error {
		val, err := lookupURL("APP_PASSWORD_CHANGED_URL")
		if err == nil && val != nil {
			c.AppPasswordChangedURL = val
		}
		return err
	},

	// APP_PASSWORD_RESET_URL is an endpoint that will be notified when an account
	// has requested a password reset. The endpoint is expected to deliver an email
	// with the given password reset token, then respond with a 2xx HTTP status.
	//
	// For security, this URL should specify https and include a basic auth username
	// and password.
	func(c *Config) error {
		val, err := lookupURL("APP_PASSWORD_RESET_URL")
		if err == nil && val != nil {
			c.AppPasswordResetURL = val
		}
		return err
	},

	// APP_PASSWORDLESS_TOKEN_URL is an endpoint that will be notified when an account
	// has requested a passwordless token. The endpoint is expected to deliver an email
	// with the given passwordless token, then respond with a 2xx HTTP status.
	//
	// For security, this URL should specify https and include a basic auth username
	// and password.
	func(c *Config) error {
		val, err := lookupURL("APP_PASSWORDLESS_TOKEN_URL")
		if err == nil && val != nil {
			c.AppPasswordlessTokenURL = val
		}
		return err
	},

	// RSA_PRIVATE_KEY is a RSA private key in PEM format. If provided as a single
	// line string, any literal \n sequences will be converted to real linebreaks.
	// When provided, it will be used for signing identity tokens, and the public
	// key will be published for audiences to verify. When not provided, AuthN will
	// generate and manage keys itself, using Redis for coordination and
	// persistence.
	func(c *Config) error {
		if str, ok := os.LookupEnv("RSA_PRIVATE_KEY"); ok {
			str = strings.Replace(str, `\n`, "\n", -1)
			block, _ := pem.Decode([]byte(str))
			key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				return err
			}
			c.IdentitySigningKey, err = private.NewKey(key)
			if err != nil {
				return err
			}
		}
		return nil
	},

	// TIME_ZONE is the IANA name of a location that should be used when calculating
	// which day it is when tracking key stats. It defaults to UTC.
	func(c *Config) error {
		name, ok := os.LookupEnv("TIME_ZONE")
		if !ok {
			name = "UTC"
		}

		tz, err := time.LoadLocation(name)
		if err != nil {
			return err
		}
		c.StatisticsTimeZone = tz
		return nil
	},

	// DAILY_ACTIVES_RETENTION is how many daily records of the number of active accounts to keep.
	// The default is 365 (~1 year).
	func(c *Config) error {
		num, err := lookupInt("DAILY_ACTIVES_RETENTION", 365)
		if err == nil {
			c.DailyActivesRetention = num
		}
		return err
	},

	// WEEKLY_ACTIVES_RETENTION is how many weekly records of the number of active accounts to keep.
	// The default is 104 (~2 years).
	func(c *Config) error {
		num, err := lookupInt("WEEKLY_ACTIVES_RETENTION", 104)
		if err == nil {
			c.WeeklyActivesRetention = num
		}
		return err
	},

	// SENTRY_DSN is a configuration string for the Sentry error reporting backend. When provided,
	// errors and panics will be reported asynchronously.
	func(c *Config) error {
		if val, ok := os.LookupEnv("SENTRY_DSN"); ok {
			c.ErrorReporterCredentials = val
			c.ErrorReporterType = ops.Sentry
		}
		return nil
	},

	// AIRBRAKE_CREDENTIALS is a configuration string for the Airbrake error reporting backend. When
	// provided, errors and panics will be reported asynchronously.
	func(c *Config) error {
		if val, ok := os.LookupEnv("AIRBRAKE_CREDENTIALS"); ok {
			c.ErrorReporterCredentials = val
			c.ErrorReporterType = ops.Airbrake
		}
		return nil
	},

	// PORT is the local port the AuthN server listens to. The default is taken from AUTHN_URL, but
	// may be different for port mapping scenarios as with containers and load balancers.
	func(c *Config) error {
		defaultPort, err := strconv.Atoi(c.AuthNURL.Port())
		val, err := lookupInt("PORT", defaultPort)
		if err == nil {
			c.ServerPort = val
		}
		return err
	},

	// PUBLIC_PORT is an extra local port the AuthN server listens to with only public routes. This
	// is useful to avoid exposing admin routes to the public, since you can configure a proxy or
	// load balancer to forward to only the appropriate port.
	func(c *Config) error {
		val, err := lookupInt("PUBLIC_PORT", 0)
		if err == nil {
			c.PublicPort = val
		}
		return err
	},

	// PROXIED is a flag that indicates AuthN is behind a proxy. When set, AuthN will read IP
	// addresses from X-FORWARDED-FOR (and similar).
	func(c *Config) error {
		val, err := lookupBool("PROXIED", false)
		if err == nil {
			c.Proxied = val
		}
		return err
	},

	// GOOGLE_OAUTH_CREDENTIALS is a credential pair in the format `id:secret`. When specified,
	// AuthN will enable routes for Google OAuth signin.
	func(c *Config) error {
		if val, ok := os.LookupEnv("GOOGLE_OAUTH_CREDENTIALS"); ok {
			credentials, err := oauth.NewCredentials(val)
			if err == nil {
				c.GoogleOauthCredentials = credentials
			}
			return err
		}
		return nil
	},

	// GITHUB_OAUTH_CREDENTIALS is a credential pair in the format `id:secret`. When specified,
	// AuthN will enable routes for GitHub OAuth signin.
	func(c *Config) error {
		if val, ok := os.LookupEnv("GITHUB_OAUTH_CREDENTIALS"); ok {
			credentials, err := oauth.NewCredentials(val)
			if err == nil {
				c.GitHubOauthCredentials = credentials
			}
			return err
		}
		return nil
	},

	// FACEBOOK_OAUTH_CREDENTIALS is a credential pair in the format `id:secret`. When specified,
	// AuthN will enable routes for Facebook OAuth signin.
	func(c *Config) error {
		if val, ok := os.LookupEnv("FACEBOOK_OAUTH_CREDENTIALS"); ok {
			credentials, err := oauth.NewCredentials(val)
			if err == nil {
				c.FacebookOauthCredentials = credentials
			}
			return err
		}
		return nil
	},

	// DISCORD_OAUTH_CREDENTIALS is a credential pair in the format `id:secret`. When specified,
	// AuthN will enable routes for Discord OAuth signin.
	func(c *Config) error {
		if val, ok := os.LookupEnv("DISCORD_OAUTH_CREDENTIALS"); ok {
			credentials, err := oauth.NewCredentials(val)
			if err == nil {
				c.DiscordOauthCredentials = credentials
			}
			return err
		}
		return nil
	},
}

// ReadEnv returns a Config struct from environment variables. It returns errors when a variable is
// malformatted or missing but required.
func ReadEnv() (*Config, error) {
	return configure(configurers)
}

// 20k iterations of PBKDF2 HMAC SHA-256
func derive(base []byte, salt string) []byte {
	return pbkdf2.Key(base, []byte(salt), 2e4, 128, sha256.New)
}
