package dbtypes

import "encoding/json"

func jsonUnmarshalInto(data []byte, out any) error { return json.Unmarshal(data, out) }
func jsonMarshal(v any) ([]byte, error)            { return json.Marshal(v) }
