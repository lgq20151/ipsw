package download

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"time"

	"github.com/99designs/keyring"
	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/PuerkitoBio/goquery"
	"github.com/apex/log"
	"github.com/pkg/errors"
)

/*
	NOTES:
		- https://github.com/picklepete/pyicloud
		- https://github.com/michaljirman/fmidevice
		- https://github.com/majd/ipatool
		- https://github.com/fastlane/fastlane
*/

const (
	downloadURL     = "https://developer.apple.com/download/"
	downloadAppsURL = "https://developer.apple.com/download/applications/"

	downloadActionURL      = "https://developer.apple.com/devcenter/download.action"
	listDownloadsActionURL = "https://developer.apple.com/services-account/QH65B2/downloadws/listDownloads.action"
	adcDownloadURL         = "https://developerservices2.apple.com/services/download"

	loginURL      = "https://idmsa.apple.com/appleauth/auth/signin"
	trustURL      = "https://idmsa.apple.com/appleauth/auth/2sv/trust"
	itcServiceKey = "https://appstoreconnect.apple.com/olympus/v1/app/config?hostname=itunesconnect.apple.com"

	olympusSessionURL = "https://appstoreconnect.apple.com/olympus/v1/session"

	userAgent = "Configurator/2.15 (Macintosh; OperatingSystem X 11.0.0; 16G29) AppleWebKit/2603.3.8"
)

const (
	ERROR_CODE_TOO_MANY_CODES_SENT      = -22981 // Too many verification codes have been sent.
	ERROR_CODE_BAD_CREDS                = -20101 // Your Apple ID or password was incorrect.
	ERROR_CODE_FAILED_TO_UPDATE_SESSION = -20528 // Error Description not available
	ERROR_CODE_BAD_VERIFICATION         = -21669 // Incorrect verification code.
	// TODO: flesh out the rest of the error codes
)

const (
	VaultName           = "ipsw-vault"
	AppName             = "io.blacktop.ipsw"
	KeychainServiceName = "ipsw-auth.service"
)

type DevConfig struct {
	// Login session
	SessionID string
	SCNT      string
	WidgetKey string
	// download config
	Proxy    string
	Insecure bool
	// download type config
	WatchList []string
	// behavior config
	SkipAll       bool
	ResumeAll     bool
	RestartAll    bool
	RemoveCommas  bool
	PreferSMS     bool
	PageSize      int
	Verbose       bool
	VaultPassword string
	ConfigDir     string
}

// App is the app object
type App struct {
	Client *http.Client

	Vault keyring.Keyring

	config *DevConfig

	authService    authService
	authOptions    authOptions
	codeRequest    authOptions
	olympusSession olympusResponse
	// header values
	xAppleIDAccountCountry string
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type session struct {
	SessionID string
	SCNT      string
	WidgetKey string
	Cookies   []*http.Cookie
}

type DevPortalAuth struct {
	Credentials credentials `json:"credentials"`
	Session     session     `json:"session"`
}

// DevDownload are all the downloads from https://developer.apple.com/download/
type DevDownload struct {
	Title string `json:"title,omitempty"`
	Build string `json:"build,omitempty"`
	URL   string `json:"url,omitempty"`
	Type  string `json:"type,omitempty"`
}

// Downloads listDownloads.action response
type Downloads struct {
	CreationTimestamp   time.Time `json:"creationTimestamp,omitempty"`
	ResultCode          int       `json:"resultCode,omitempty"`
	UserLocale          string    `json:"userLocale,omitempty"`
	ProtocolVersion     string    `json:"protocolVersion,omitempty"`
	RequestURL          string    `json:"requestUrl,omitempty"`
	ResponseID          string    `json:"responseId,omitempty"`
	HTTPResponseHeaders struct {
		SetCookie string `json:"Set-Cookie,omitempty"`
	} `json:"httpResponseHeaders,omitempty"`
	Downloads []dload
}

type authService struct {
	URL string `json:"authServiceUrl,omitempty"`
	Key string `json:"authServiceKey,omitempty"`
}

type auth struct {
	AccountName string   `json:"accountName,omitempty"`
	Password    string   `json:"password,omitempty"`
	RememberMe  bool     `json:"rememberMe,omitempty"`
	TrustTokens []string `json:"trust_tokens,omitempty"`
}

type trustedPhoneNumber struct {
	ID                 int    `json:"id,omitempty"`
	ObfuscatedNumber   string `json:"obfuscatedNumber,omitempty"`
	PushMode           string `json:"pushMode,omitempty"`
	NumberWithDialCode string `json:"numberWithDialCode,omitempty"`
}

type securityCode struct {
	Code                  string `json:"code,omitempty"`
	Length                int    `json:"length,omitempty"`
	TooManyCodesSent      bool   `json:"tooManyCodesSent,omitempty"`
	TooManyCodesValidated bool   `json:"tooManyCodesValidated,omitempty"`
	SecurityCodeLocked    bool   `json:"securityCodeLocked,omitempty"`
	SecurityCodeCooldown  bool   `json:"securityCodeCooldown,omitempty"`
}

type authOptions struct {
	TrustedDeviceCount              int                  `json:"trustedDeviceCount,omitempty"`
	OtherTrustedDeviceClass         string               `json:"otherTrustedDeviceClass,omitempty"`
	TrustedPhoneNumbers             []trustedPhoneNumber `json:"trustedPhoneNumbers,omitempty"`
	PhoneNumber                     trustedPhoneNumber   `json:"phoneNumber,omitempty"`
	TrustedPhoneNumber              trustedPhoneNumber   `json:"trustedPhoneNumber,omitempty"`
	SecurityCode                    securityCode         `json:"securityCode,omitempty"`
	Mode                            string               `json:"mode,omitempty"`
	Type                            string               `json:"type,omitempty"`
	AuthenticationType              string               `json:"authenticationType,omitempty"`
	RecoveryURL                     string               `json:"recoveryUrl,omitempty"`
	CantUsePhoneNumberURL           string               `json:"cantUsePhoneNumberUrl,omitempty"`
	RecoveryWebURL                  string               `json:"recoveryWebUrl,omitempty"`
	RepairPhoneNumberURL            string               `json:"repairPhoneNumberUrl,omitempty"`
	RepairPhoneNumberWebURL         string               `json:"repairPhoneNumberWebUrl,omitempty"`
	AboutTwoFactorAuthenticationURL string               `json:"aboutTwoFactorAuthenticationUrl,omitempty"`
	AutoVerified                    bool                 `json:"autoVerified,omitempty"`
	ShowAutoVerificationUI          bool                 `json:"showAutoVerificationUI,omitempty"`
	ManagedAccount                  bool                 `json:"managedAccount,omitempty"`
	Hsa2Account                     bool                 `json:"hsa2Account,omitempty"`
	RestrictedAccount               bool                 `json:"restrictedAccount,omitempty"`
	SupportsRecovery                bool                 `json:"supportsRecovery,omitempty"`
	SupportsCustodianRecovery       bool                 `json:"supportsCustodianRecovery,omitempty"`
	ServiceErrors                   []serviceError       `json:"serviceErrors,omitempty"`
	NoTrustedDevices                bool                 `json:"noTrustedDevices,omitempty"`
}

type scode struct {
	Code string `json:"code,omitempty"`
}
type phoneNumber struct {
	ID int `json:"id"`
}
type phone struct {
	SecurityCode scode       `json:"securityCode,omitempty"`
	Number       phoneNumber `json:"phoneNumber,omitempty"`
	Mode         string      `json:"mode,omitempty"`
}
type trustedDevice struct {
	SecurityCode scode `json:"securityCode,omitempty"`
}

type signInResponse struct {
	AuthType string `json:"authType,omitempty"`
}

type serviceError struct {
	Code              string `json:"code,omitempty"`
	Title             string `json:"title,omitempty"`
	Message           string `json:"message,omitempty"`
	SuppressDismissal bool   `json:"suppressDismissal,omitempty"`
}

type serviceErrorsResponse struct {
	Errors   []serviceError `json:"service_errors,omitempty"`
	HasError bool           `json:"hasError,omitempty"`
}

type olympusProvider struct {
	ID           int      `json:"providerId,omitempty"`
	PublicID     string   `json:"publicProviderId,omitempty"`
	Name         string   `json:"name,omitempty"`
	ContentTypes []string `json:"contentTypes,omitempty"`
	SubType      string   `json:"subType,omitempty"`
	PLA          []struct {
		ID                       string   `json:"id,omitempty"`
		Version                  string   `json:"version,omitempty"`
		Types                    []string `json:"types,omitempty"`
		ContractCountryOfOrigins []string `json:"contractCountryOfOrigins,omitempty"`
	} `json:"pla,omitempty"`
}

type olympusResponse struct {
	User struct {
		FullNAme  string `json:"fullName,omitempty"`
		FirstName string `json:"firstName,omitempty"`
		LastName  string `json:"lastName,omitempty"`
		Email     string `json:"emailAddress,omitempty"`
		PrsID     string `json:"prsId,omitempty"`
	} `json:"user,omitempty"`
	Provider           olympusProvider   `json:"provider,omitempty"`
	Theme              string            `json:"theme,omitempty"`
	AvailableProviders []olympusProvider `json:"availableProviders,omitempty"`
	BackingType        string            `json:"backingType,omitempty"`
	BackingTypes       []string          `json:"backingTypes,omitempty"`
	Roles              []string          `json:"roles,omitempty"`
	UnverifiedRoles    []string          `json:"unverifiedRoles,omitempty"`
	FeatureFlags       []string          `json:"featureFlags,omitempty"`
	AgreeToTerms       bool              `json:"agreeToTerms,omitempty"`
	TermsSignatures    []string          `json:"termsSignatures,omitempty"`
	PublicUserID       string            `json:"publicUserId,omitempty"`
	OfacState          any               `json:"ofacState,omitempty"`
	CreationDate       int               `json:"creationDate,omitempty"`
}

type category struct {
	ID        int    `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	SortOrder int    `json:"sortOrder,omitempty"`
}

type fformat struct {
	Extension   string `json:"extension,omitempty"`
	Description string `json:"description,omitempty"`
}

type dfile struct {
	Filename     string  `json:"filename,omitempty"`
	DisplayName  string  `json:"displayName,omitempty"`
	RemotePath   string  `json:"remotePath,omitempty"`
	FileSize     int     `json:"fileSize,omitempty"`
	SortOrder    int     `json:"sortOrder,omitempty"`
	DateCreated  string  `json:"dateCreated,omitempty"`
	DateModified string  `json:"dateModified,omitempty"`
	FileFormat   fformat `json:"fileFormat,omitempty"`
}

func (d dfile) URL() string {
	return fmt.Sprintf("%s?path=%s", downloadActionURL, d.RemotePath)
}

type dload struct {
	Name          string     `json:"name,omitempty"`
	Description   string     `json:"description,omitempty"`
	IsReleased    int        `json:"isReleased,omitempty"`
	DatePublished string     `json:"datePublished,omitempty"`
	DateCreated   string     `json:"dateCreated,omitempty"`
	DateModified  string     `json:"dateModified,omitempty"`
	Categories    []category `json:"categories,omitempty"`
	Files         []dfile    `json:"files,omitempty"`
}

// NewDevPortal returns a new instance of teh dev portal app
func NewDevPortal(config *DevConfig) *App {
	jar, _ := cookiejar.New(nil)

	app := App{
		Client: &http.Client{
			Jar: jar,
			Transport: &http.Transport{
				Proxy:           GetProxy(config.Proxy),
				TLSClientConfig: &tls.Config{InsecureSkipVerify: config.Insecure},
			},
		},
		config: config,
	}

	return &app
}

// Init app
func (app *App) Init() (err error) {
	// create credential vault (if it doesn't exist)
	app.Vault, err = keyring.Open(keyring.Config{
		ServiceName:                    KeychainServiceName,
		KeychainSynchronizable:         false,
		KeychainAccessibleWhenUnlocked: true,
		KeychainTrustApplication:       true,
		FileDir:                        app.config.ConfigDir,
		FilePasswordFunc: func(string) (string, error) {
			if len(app.config.VaultPassword) == 0 {
				msg := "Enter a password to decrypt your credentials vault: " + filepath.Join(app.config.ConfigDir, VaultName)
				if _, err := os.Stat(filepath.Join(app.config.ConfigDir, VaultName)); errors.Is(err, os.ErrNotExist) {
					msg = "Enter a password to encrypt your credentials to vault: " + filepath.Join(app.config.ConfigDir, VaultName)
				}
				prompt := &survey.Password{
					Message: msg,
				}
				if err := survey.AskOne(prompt, &app.config.VaultPassword); err != nil {
					if err == terminal.InterruptErr {
						log.Warn("Exiting...")
						os.Exit(0)
					}
					return "", err
				}
			}
			return app.config.VaultPassword, nil
		},
	})
	if err != nil {
		return fmt.Errorf("failed to open vault: %s", err)
	}

	return nil
}

func (app *App) GetSessionID() string {
	return app.config.SessionID
}
func (app *App) GetSCNT() string {
	return app.config.SCNT
}
func (app *App) GetWidgetKey() string {
	return app.config.WidgetKey
}

// Login to Apple
func (app *App) Login(username, password string) error {
	if len(username) == 0 || len(password) == 0 {
		creds, err := app.Vault.Get(VaultName)
		if err != nil { // failed to get credentials from vault (prompt user for credentials)
			log.Errorf("failed to get credentials from vault: %v", err)
			// get username
			if len(username) == 0 {
				prompt := &survey.Input{
					Message: "Please type your username:",
				}
				if err := survey.AskOne(prompt, &username); err != nil {
					if err == terminal.InterruptErr {
						log.Warn("Exiting...")
						os.Exit(0)
					}
					return err
				}
			}
			// get password
			if len(password) == 0 {
				prompt := &survey.Password{
					Message: "Please type your password:",
				}
				if err := survey.AskOne(prompt, &password); err != nil {
					if err == terminal.InterruptErr {
						log.Warn("Exiting...")
						os.Exit(0)
					}
					return err
				}
			}
			// save credentials to vault
			dat, err := json.Marshal(&DevPortalAuth{
				Credentials: credentials{
					Username: username,
					Password: password,
				},
			})
			if err != nil {
				return fmt.Errorf("failed to marshal keychain credentials: %v", err)
			}
			app.Vault.Set(keyring.Item{
				Key:         VaultName,
				Data:        dat,
				Label:       AppName,
				Description: "application password",
			})
		} else { // credentials found in vault
			var auth DevPortalAuth
			if err := json.Unmarshal(creds.Data, &auth); err != nil {
				return fmt.Errorf("failed to unmarshal keychain credentials: %v", err)
			}
			username = auth.Credentials.Username
			password = auth.Credentials.Password
			auth = DevPortalAuth{}
		}
	}

	if err := app.loadSession(); err != nil { // load previous session (if error, login)
		if err := app.getITCServiceKey(); err != nil {
			return err
		}

		return app.signIn(username, password)
	}

	return nil
}

func (app *App) updateRequestHeaders(req *http.Request) {
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	req.Header.Set("X-Apple-Id-Session-Id", app.config.SessionID)
	req.Header.Set("X-Apple-Widget-Key", app.config.WidgetKey)
	req.Header.Set("Scnt", app.config.SCNT)

	req.Header.Add("User-Agent", userAgent)
}

func (app *App) getITCServiceKey() error {

	response, err := app.Client.Get(itcServiceKey)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(body, &app.authService); err != nil {
		return fmt.Errorf("failed to deserialize response body JSON: %v", err)
	}

	log.Debugf("GET iTC Service Key: (%d):\n%s\n", response.StatusCode, string(body))

	if response.StatusCode != 200 {
		return fmt.Errorf("failed to get iTC Service Key: response received %s", response.Status)
	}

	app.config.WidgetKey = app.authService.Key

	return nil
}

func (app *App) signIn(username, password string) error {
	buf := new(bytes.Buffer)

	json.NewEncoder(buf).Encode(&auth{
		AccountName: username,
		Password:    password,
		RememberMe:  true,
	})

	req, err := http.NewRequest("POST", loginURL, buf)
	if err != nil {
		return fmt.Errorf("failed to create http POST request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Apple-Widget-Key", app.config.WidgetKey)
	req.Header.Add("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/javascript")

	response, err := app.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	log.Debugf("POST Login: (%d):\n%s\n", response.StatusCode, string(body))

	if response.StatusCode == 409 {
		app.xAppleIDAccountCountry = response.Header.Get("X-Apple-Id-Account-Country")
		app.config.SessionID = response.Header.Get("X-Apple-Id-Session-Id")
		app.config.SCNT = response.Header.Get("Scnt")

		if err := app.getAuthOptions(); err != nil {
			return err
		}

		phoneID := 1
		codeType := "phone"

		// SMS was sent automatically
		if app.authOptions.NoTrustedDevices && len(app.authOptions.TrustedPhoneNumbers) == 1 {
			codeType = "phone"
			// User needs to choose a phone to send to
		} else if app.authOptions.NoTrustedDevices && len(app.authOptions.TrustedPhoneNumbers) > 1 {
			codeType = "phone"
			phoneNumber := 0
			var choices []string
			for _, num := range app.authOptions.TrustedPhoneNumbers {
				choices = append(choices, num.NumberWithDialCode)
			}
			prompt := &survey.Select{
				Message: "Choose a phone number to send the SMS code to:",
				Options: choices,
			}
			if err := survey.AskOne(prompt, &phoneNumber); err != nil {
				if err == terminal.InterruptErr {
					log.Warn("Exiting...")
					os.Exit(0)
				}
				return err
			}
			phoneID = app.authOptions.TrustedPhoneNumbers[phoneNumber].ID
			if err := app.requestCode(phoneID); err != nil {
				return err
			}

		} else { // Code is shown on trusted devices
			codeType = "trusteddevice"
			if app.config.PreferSMS {
				codeType = "phone"
				if err := app.requestCode(1); err != nil {
					if app.codeRequest.SecurityCode.TooManyCodesSent {
						codeType = "trusteddevice"
						log.Warn("you must use the trusted device code (SMS codes have been disabled on your account)")
					} else {
						return err
					}
				}
			}
		}

		code := ""
		// USED FOR DEBUGGING
		// cwd, err := os.Getwd()
		// if err != nil {
		// 	log.Error(err.Error())
		// }
		// cpath := filepath.Join(cwd, "..", "..", "test-caches", "CODE")
		// fmt.Printf("Enter code in file (%s): ", cpath)
		// for {
		// 	codeSTR, err := os.ReadFile(cpath)
		// 	if err != nil {
		// 		return err
		// 	}
		// 	if len(codeSTR) > 0 {
		// 		code = string(codeSTR)
		// 		// remove code for next time
		// 		defer func() {
		// 			if err := os.WriteFile(cpath, []byte(""), 0660); err != nil {
		// 				log.Error(err.Error())
		// 			}
		// 		}()
		// 		break
		// 	}
		// }
		if len(code) == 0 {
			prompt := &survey.Password{
				Message: "Please type your verification code:",
			}
			if err := survey.AskOne(prompt, &code); err != nil {
				if err == terminal.InterruptErr {
					log.Warn("Exiting...")
					os.Exit(0)
				}
				return err
			}
		}

		if err := app.verifyCode(codeType, code, phoneID); err != nil {
			return err
		}

		if err := app.trustSession(); err != nil {
			return err
		}

	} else if response.StatusCode != 200 {
		return fmt.Errorf("failed to sign in; expected status code 409 (for two factor auth): response received %s", response.Status)
	}

	if err := app.storeSession(response); err != nil {
		return err
	}

	return nil
}

func (app *App) getAuthOptions() error {

	req, err := http.NewRequest("GET", "https://idmsa.apple.com/appleauth/auth", nil)
	if err != nil {
		return fmt.Errorf("failed to create http GET request: %v", err)
	}
	app.updateRequestHeaders(req)

	response, err := app.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	log.Debugf("GET getAuthOptions (%d):\n%s\n", response.StatusCode, string(body))

	if err := json.Unmarshal(body, &app.authOptions); err != nil {
		return fmt.Errorf("failed to deserialize response body JSON: %v", err)
	}

	if 200 > response.StatusCode || 300 <= response.StatusCode {
		return fmt.Errorf("failed to get auth options: response received %s", response.Status)
	}

	return nil
}

func (app *App) requestCode(phoneID int) error {
	buf := new(bytes.Buffer)

	json.NewEncoder(buf).Encode(&phone{
		Number: phoneNumber{
			ID: phoneID,
		},
		Mode: "sms",
	})

	req, err := http.NewRequest("PUT", "https://idmsa.apple.com/appleauth/auth/verify/phone", buf)
	if err != nil {
		return fmt.Errorf("failed to create http PUT request: %v", err)
	}
	app.updateRequestHeaders(req)

	response, err := app.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	log.Debugf("PUT requestCode (%d):\n%s\n", response.StatusCode, string(body))

	if err := json.Unmarshal(body, &app.codeRequest); err != nil {
		return fmt.Errorf("failed to deserialize response body JSON: %v", err)
	}

	if 200 > response.StatusCode || 300 <= response.StatusCode {
		var errStr string
		if app.codeRequest.ServiceErrors != nil {
			for _, svcErr := range app.codeRequest.ServiceErrors {
				errStr += fmt.Sprintf(": %s", svcErr.Message)
			}
			return fmt.Errorf("failed to verify code: response received %s%s", response.Status, errStr)
		}

		if response.StatusCode == 423 { // code rate limiting
			log.Error(errStr)
			return nil
		}

		return fmt.Errorf("failed to verify code: response received %s%s", response.Status, errStr)
	}

	return nil
}

func (app *App) verifyCode(codeType, code string, phoneID int) error {
	buf := new(bytes.Buffer)

	if codeType == "phone" {
		json.NewEncoder(buf).Encode(&phone{
			Number: phoneNumber{
				ID: phoneID,
			},
			Mode: "sms",
			SecurityCode: scode{
				Code: code,
			},
		})
	} else { // trusteddevice
		json.NewEncoder(buf).Encode(&trustedDevice{
			SecurityCode: scode{
				Code: code,
			},
		})
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("https://idmsa.apple.com/appleauth/auth/verify/%s/securitycode", codeType), buf)
	if err != nil {
		return fmt.Errorf("failed to create http POST request: %v", err)
	}
	app.updateRequestHeaders(req)

	response, err := app.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	log.Debugf("POST verifyCode (%d):\n%s\n", response.StatusCode, string(body))

	if 200 > response.StatusCode || 300 <= response.StatusCode {
		if len(body) > 0 {
			var svcErr serviceErrorsResponse
			if err := json.Unmarshal(body, &svcErr); err != nil {
				return fmt.Errorf("failed to deserialize response body JSON: %v", err)
			}
			if svcErr.HasError {
				var errStr string
				for _, svcErr := range svcErr.Errors {
					errStr += fmt.Sprintf(": %s", svcErr.Message)
				}
				return fmt.Errorf("failed to verify code: response received %s%s", response.Status, errStr)
			}
		}

		return fmt.Errorf("failed to verify code: response received %s", response.Status)
	}

	return nil
}

// trustSession tells Apple to trust computer for 2FA
func (app *App) trustSession() error {

	req, err := http.NewRequest("GET", trustURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create http GET request: %v", err)
	}
	app.updateRequestHeaders(req)

	response, err := app.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	log.Debugf("GET trustSession: (%d):\n%s\n", response.StatusCode, string(body))

	if 200 > response.StatusCode || 300 <= response.StatusCode {
		if len(body) > 0 {
			var svcErr serviceErrorsResponse
			if err := json.Unmarshal(body, &svcErr); err != nil {
				return fmt.Errorf("failed to deserialize response body JSON: %v", err)
			}
			if svcErr.HasError {
				var errStr string
				for _, svcErr := range svcErr.Errors {
					errStr += fmt.Sprintf(": %s", svcErr.Message)
				}
				return fmt.Errorf("failed to update to trusted session: response received %s%s", response.Status, errStr)
			}
		}
		return fmt.Errorf("failed to update to trusted session: response received %s", response.Status)
	}

	return nil
}

func (app *App) refreshSession() error {
	// check if olympus session is expired (prevents login rate limiting)
	if err := app.getOlympusSession(); err != nil {
		// if olympus session is expired, we need to login again
		return app.Login("", "")
	}
	return nil
}

func (app *App) getOlympusSession() error {

	req, err := http.NewRequest("GET", olympusSessionURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create http GET request: %v", err)
	}
	app.updateRequestHeaders(req)

	response, err := app.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	log.Debugf("GET getOlympusSession (%d):\n%s\n", response.StatusCode, string(body))

	if 200 > response.StatusCode || 300 <= response.StatusCode {
		return fmt.Errorf("failed to get auth options: response received %s", response.Status)
	}

	if err := json.Unmarshal(body, &app.olympusSession); err != nil {
		var wat any
		json.Unmarshal(body, &wat)
		log.Errorf("%#v", wat)
		return fmt.Errorf("failed to deserialize response body JSON: %v", err)
	}

	return nil
}

func (app *App) storeSession(response *http.Response) error {
	// get dev auth from vault
	sess, err := app.Vault.Get(VaultName)
	if err != nil {
		return fmt.Errorf("failed to get dev auth from vault: %v", err)
	}

	var auth DevPortalAuth
	if err := json.Unmarshal(sess.Data, &auth); err != nil {
		return fmt.Errorf("failed to unmarshal dev auth: %v", err)
	}

	auth.Session = session{
		SessionID: app.GetSessionID(),
		SCNT:      app.GetSCNT(),
		WidgetKey: app.GetWidgetKey(),
		Cookies:   app.Client.Jar.Cookies(&url.URL{Scheme: "https", Host: "idmsa.apple.com"}),
	}

	// save dev auth to vault
	data, err := json.Marshal(&auth)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %v", err)
	}

	// clear dev auth mem
	auth = DevPortalAuth{}

	app.Vault.Set(keyring.Item{
		Key:         VaultName,
		Data:        data,
		Label:       AppName,
		Description: "application password",
	})

	return nil
}

func (app *App) loadSession() error {
	// get dev auth from vault
	sess, err := app.Vault.Get(VaultName)
	if err != nil {
		return fmt.Errorf("failed to get dev auth from vault: %v", err)
	}

	var auth DevPortalAuth
	if err := json.Unmarshal(sess.Data, &auth); err != nil {
		return fmt.Errorf("failed to unmarshal dev auth: %v", err)
	}

	app.config.SessionID = auth.Session.SessionID
	app.config.SCNT = auth.Session.SCNT
	app.config.WidgetKey = auth.Session.WidgetKey
	app.Client.Jar.SetCookies(&url.URL{Scheme: "https", Host: "idmsa.apple.com"}, auth.Session.Cookies)

	// clear dev auth mem
	auth = DevPortalAuth{}

	if err := app.getOlympusSession(); err != nil {
		return err
	}

	return nil
}

// Watch watches for NEW downloads
func (app *App) Watch() error {

	var prevIPSWs map[string][]DevDownload

	for {
		// scrape dev portal
		ipsws, err := app.getDevDownloads()
		if err != nil {
			return err
		}

		// check for NEW downloads
		if reflect.DeepEqual(prevIPSWs, ipsws) {
			time.Sleep(5 * time.Minute)

			if err := app.refreshSession(); err != nil {
				return err
			}

			continue

		} else {
			prevIPSWs = ipsws
		}

		for version := range ipsws {
			for _, watchPattern := range app.config.WatchList {
				re, err := regexp.Compile(watchPattern)
				if err != nil {
					return fmt.Errorf("failed to compile regex watch pattern '%s': %v", watchPattern, err)
				}
				if re.MatchString(version) {
					for _, ipsw := range ipsws[version] {
						if err := app.Download(ipsw.URL); err != nil {
							log.Errorf("failed to download %s: %v", ipsw.URL, err)
						}
					}
				}
			}
		}
	}
}

// DownloadPrompt prompts the user for which files to download from https://developer.apple.com/download
func (app *App) DownloadPrompt(downloadType string) error {
	switch downloadType {
	case "more":
		dloads, err := app.getDownloads()
		if err != nil {
			return fmt.Errorf("failed to get the '%s' downloads: %v", downloadType, err)
		}

		var choices []string
		for _, dl := range dloads.Downloads {
			choices = append(choices, fmt.Sprintf("%s (%s)", dl.Name, dl.DateCreated))
		}

		dfiles := []int{}
		prompt := &survey.MultiSelect{
			Message:  "Select what file(s) to download:",
			Options:  choices,
			PageSize: app.config.PageSize,
		}
		if err := survey.AskOne(prompt, &dfiles); err == terminal.InterruptErr {
			log.Warn("Exiting...")
			os.Exit(0)
		}

		for _, idx := range dfiles {
			for _, f := range dloads.Downloads[idx].Files {
				app.Download(f.URL())
			}
		}
	default:
		ipsws, err := app.getDevDownloads()
		if err != nil {
			return fmt.Errorf("failed to get the '%s' downloads: %v", downloadType, err)
		}

		versions := make([]string, 0, len(ipsws))
		for v := range ipsws {
			versions = append(versions, v)
		}
		sort.Strings(versions)

		version := ""
		promptVer := &survey.Select{
			Message:  "Choose an OS version:",
			Options:  versions,
			PageSize: 15,
		}
		if err := survey.AskOne(promptVer, &version); err != nil {
			if err == terminal.InterruptErr {
				log.Warn("Exiting...")
				os.Exit(0)
			}
			return err
		}

		if len(ipsws[version]) > 1 {
			var choices []string
			for _, ipsw := range ipsws[version] {
				choices = append(choices, ipsw.Title)
			}

			dfiles := []int{}
			prompt := &survey.MultiSelect{
				Message:  "Select what file(s) to download:",
				Options:  choices,
				PageSize: app.config.PageSize,
			}
			if err := survey.AskOne(prompt, &dfiles); err != nil {
				if err == terminal.InterruptErr {
					log.Warn("Exiting...")
					os.Exit(0)
				}
				return err
			}

			for _, df := range dfiles {
				app.Download(ipsws[version][df].URL)
			}
		} else {
			app.Download(ipsws[version][0].URL)
		}
	}

	return nil
}

// Download downloads a file that requires a valid dev portal session
func (app *App) Download(url string) error {

	// proxy, insecure are null because we override the client below
	downloader := NewDownload(
		app.config.Proxy,
		app.config.Insecure,
		app.config.SkipAll,
		app.config.ResumeAll,
		app.config.RestartAll,
		false,
		app.config.Verbose,
	)
	// use authenticated client
	downloader.client = app.Client

	destName := getDestName(url, app.config.RemoveCommas)
	if _, err := os.Stat(destName); os.IsNotExist(err) {

		log.WithFields(log.Fields{
			"file": destName,
		}).Info("Downloading")

		// download file
		downloader.URL = url
		downloader.DestName = destName

		err = downloader.Do()
		if err != nil {
			return fmt.Errorf("failed to download file: %v", err)
		}

	} else {
		log.Warnf("file already exists: %s", destName)
	}

	return nil
}

func (app *App) GetDownloadsAsJSON(downloadType string, pretty bool) ([]byte, error) {
	switch downloadType {
	case "more":
		dloads, err := app.getDownloads()
		if err != nil {
			return nil, fmt.Errorf("failed to get the '%s' downloads: %v", downloadType, err)
		}
		if pretty {
			return json.MarshalIndent(dloads, "", "    ")
		}
		return json.Marshal(dloads)
	default:
		ipsws, err := app.getDevDownloads()
		if err != nil {
			return nil, fmt.Errorf("failed to get developer downloads: %v", err)
		}
		if pretty {
			return json.MarshalIndent(ipsws, "", "    ")
		}
		return json.Marshal(ipsws)
	}
}

// getDownloads returns all the downloads in "More Downloads" - https://developer.apple.com/download/all/
func (app *App) getDownloads() (*Downloads, error) {
	var downloads Downloads

	req, err := http.NewRequest("POST", listDownloadsActionURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create http POST request: %v", err)
	}
	req.Header.Set("Accept", "application/json")

	response, err := app.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(body, &downloads); err != nil {
		return nil, fmt.Errorf("failed to deserialize response body JSON: %v", err)
	}

	log.Debugf("Get Downloads: (%d):\n%s\n", response.StatusCode, string(body))

	// sort by file name
	// sort.Slice(downloads.Downloads, func(i, j int) bool {
	// 	return downloads.Downloads[i].Name < downloads.Downloads[j].Name
	// })

	// sort by date created
	sort.Slice(downloads.Downloads, func(i, j int) bool {
		layout := "01/02/06 15:04"
		di, _ := time.Parse(layout, downloads.Downloads[i].DateCreated)
		dj, _ := time.Parse(layout, downloads.Downloads[j].DateCreated)
		return dj.Before(di)
	})

	return &downloads, nil
}

// getDevDownloads scrapes the https://developer.apple.com/download/ page for links
func (app *App) getDevDownloads() (map[string][]DevDownload, error) {
	ipsws := make(map[string][]DevDownload)

	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create http GET request: %v", err)
	}

	response, err := app.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to do GET request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		return nil, fmt.Errorf("failed to GET %s: response received %s", downloadURL, response.Status)
	}

	doc, err := goquery.NewDocumentFromReader(response.Body)
	if err != nil {
		return nil, err
	}

	doc.Find("#main > section.section.section-downloads > div").Each(func(i int, s *goquery.Selection) {
		s.Find(".row").Each(func(index int, row *goquery.Selection) {

			// Get ALL the iOS ipsw links
			row.Find("ul.ios-list").Each(func(_ int, ul *goquery.Selection) {
				ul.Find("ul > li").Each(func(_ int, li *goquery.Selection) {
					a := li.Find("a[href]")
					href, _ := a.Attr("href")
					p := li.Find("p")
					version := ul.Parent().Parent().Parent().Find("h3")
					ipsws[version.Text()] = append(ipsws[version.Text()], DevDownload{
						Title: strings.ReplaceAll(a.Text(), "\u00a0", " "),
						Build: p.Text(),
						URL:   href,
						Type:  "ios",
					})
				})
			})

			// Get ALL the tvOS ipsw links
			row.Find("ul.tvos-list").Each(func(_ int, ul *goquery.Selection) {
				ul.Find("li").Each(func(_ int, li *goquery.Selection) {
					a := li.Find("a[href]")
					href, _ := a.Attr("href")
					p := li.Find("p")
					version := ul.Parent().Parent().Parent().Find("h3")
					ipsws[version.Text()] = append(ipsws[version.Text()], DevDownload{
						Title: strings.ReplaceAll(a.Text(), "\u00a0", " "),
						Build: p.Text(),
						URL:   href,
						// Type:  "tvos", TODO: this is commented out because macOS is also labeled as tvos
					})
				})
			})

		})
	})

	return ipsws, nil
}
