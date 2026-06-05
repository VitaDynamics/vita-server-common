package mobile

import (
	"fmt"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	dypnsapi20170525 "github.com/alibabacloud-go/dypnsapi-20170525/v3/client"
	util "github.com/alibabacloud-go/tea-utils/v2/service"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/aliyun/credentials-go/credentials"
)

type CreateClientParams struct {
	EndPoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	Token           string `yaml:"token"`
}

// GetMobile retrieves the phone number from an access token using Aliyun Dypns API.
//
// @param token The access token obtained from Aliyun one-click login SDK (App端SDK获取的登录Token)
// @return phone The mobile phone number if successful, empty string if failed
// @return error The error message if the request failed, nil if successful
func GetMobile(params *CreateClientParams) (string, error) {
	if params.Token == "" {
		return "", fmt.Errorf("token cannot be empty")
	}

	// Initialize Aliyun client
	client, err := createClient(params)
	if err != nil {
		return "", fmt.Errorf("failed to create Dypns client: %v", err)
	}

	// Create GetMobile request
	getMobileRequest := &dypnsapi20170525.GetMobileRequest{
		AccessToken: tea.String(params.Token),
	}

	// Use RuntimeOptions for proper error handling
	runtime := &util.RuntimeOptions{}

	// Call GetMobile API with proper error handling
	var resp *dypnsapi20170525.GetMobileResponse
	tryErr := func() error {
		defer func() {
			if r := tea.Recover(recover()); r != nil {
				err = r
			}
		}()

		response, err := client.GetMobileWithOptions(getMobileRequest, runtime)
		if err != nil {
			return err
		}
		resp = response
		return nil
	}()

	if tryErr != nil {
		// Handle SDK errors
		sdkError := &tea.SDKError{}
		if t, ok := tryErr.(*tea.SDKError); ok {
			sdkError = t
		} else {
			sdkError.Message = tea.String(tryErr.Error())
		}

		errMsg := tea.StringValue(sdkError.Message)
		return "", fmt.Errorf("GetMobile API call failed: %s", errMsg)
	}

	// Check response
	if resp == nil || resp.Body == nil {
		return "", fmt.Errorf("empty response from GetMobile API")
	}

	// Check response code
	if resp.Body.Code == nil || tea.StringValue(resp.Body.Code) != "OK" {
		code := ""
		message := ""
		if resp.Body.Code != nil {
			code = tea.StringValue(resp.Body.Code)
		}
		if resp.Body.Message != nil {
			message = tea.StringValue(resp.Body.Message)
		}
		return "", fmt.Errorf("GetMobile API returned error - Code: %s, Message: %s", code, message)
	}

	// Extract mobile number from response
	if resp.Body.GetMobileResultDTO == nil || resp.Body.GetMobileResultDTO.Mobile == nil {
		return "", fmt.Errorf("no mobile number in response")
	}

	mobile := tea.StringValue(resp.Body.GetMobileResultDTO.Mobile)
	return mobile, nil
}

// createClient initializes Aliyun Dypns client using access_key credentials
//
// Reference: https://help.aliyun.com/document_detail/378661.html
func createClient(params *CreateClientParams) (*dypnsapi20170525.Client, error) {
	// Get mobile config
	if params == nil {
		return nil, fmt.Errorf("mobile config not found in global configuration")
	}

	// Validate credentials
	if params.AccessKeyID == "" || params.AccessKeySecret == "" {
		return nil, fmt.Errorf("AccessKeyID or AccessKeySecret not configured")
	}

	// Create access_key credential with explicit type
	credentialsConfig := new(credentials.Config).
		SetType("access_key").
		SetAccessKeyId(params.AccessKeyID).
		SetAccessKeySecret(params.AccessKeySecret)

	cred, err := credentials.NewCredential(credentialsConfig)
	if err != nil {
		return nil, err
	}

	config := &openapi.Config{
		Credential: cred,
	}
	// Dypns API endpoint
	config.Endpoint = tea.String(params.EndPoint)

	client, err := dypnsapi20170525.NewClient(config)
	if err != nil {
		return nil, err
	}

	return client, nil
}
