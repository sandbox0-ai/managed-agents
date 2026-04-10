package managedagents

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"
	"github.com/sandbox0-ai/managed-agent/internal/apicontract"
)

func decodeJSONBody(c *gin.Context, target any) error {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return decodeJSONBytes(body, target)
}

func decodeJSONBytes(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return errors.New("request body must contain a single JSON object")
}

func readJSONBody(c *gin.Context) ([]byte, error) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func decodeContractJSONBody(c *gin.Context, schemaName string, target any) error {
	body, err := readValidatedContractJSONBody(c, schemaName)
	if err != nil {
		return err
	}
	return decodeJSONBytes(body, target)
}

func readValidatedContractJSONBody(c *gin.Context, schemaName string) ([]byte, error) {
	body, err := readJSONBody(c)
	if err != nil {
		return nil, err
	}
	if err := validateJSONBodyAgainstContractSchema(schemaName, body); err != nil {
		return nil, err
	}
	return body, nil
}

var (
	contractSchemaDocOnce sync.Once
	contractSchemaDoc     *openapi3.T
	contractSchemaDocErr  error
)

func contractOpenAPIDoc() (*openapi3.T, error) {
	contractSchemaDocOnce.Do(func() {
		loader := openapi3.NewLoader()
		contractSchemaDoc, contractSchemaDocErr = loader.LoadFromData(apicontract.ManagedAgentOpenAPI)
	})
	return contractSchemaDoc, contractSchemaDocErr
}

func validateJSONBodyAgainstContractSchema(schemaName string, body []byte) error {
	doc, err := contractOpenAPIDoc()
	if err != nil {
		return err
	}
	ref := doc.Components.Schemas[schemaName]
	if ref == nil || ref.Value == nil {
		return errors.New("contract schema not found: " + schemaName)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != nil {
		if !errors.Is(err, io.EOF) {
			return err
		}
	}
	return ref.Value.VisitJSON(value, openapi3.VisitAsRequest())
}
