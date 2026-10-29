package nostr

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCountEnvelopeFromJSONCount(t *testing.T) {
	env := &CountEnvelope{}
	require.NoError(t, env.FromJSON(`["COUNT","sub1",{"count":42}]`))
	assert.Equal(t, "sub1", env.SubscriptionID)
	require.NotNil(t, env.Count)
	assert.Equal(t, int64(42), *env.Count)
	assert.Nil(t, env.HyperLogLog)
}

func TestCountEnvelopeFromJSONHyperLogLog(t *testing.T) {
	hll := strings.Repeat("ab", 256)

	env := &CountEnvelope{}
	require.NoError(t, env.FromJSON(`["COUNT","sub1",{"count":7,"hll":"`+hll+`"}]`))
	require.NotNil(t, env.Count)
	assert.Equal(t, int64(7), *env.Count)
	require.Len(t, env.HyperLogLog, 256)
	assert.Equal(t, byte(0xab), env.HyperLogLog[0])
	assert.Equal(t, byte(0xab), env.HyperLogLog[255])
}

func TestCountEnvelopeFromJSONFilter(t *testing.T) {
	env := &CountEnvelope{}
	require.NoError(t, env.FromJSON(`["COUNT","sub1",{"kinds":[1],"limit":10}]`))
	assert.Equal(t, "sub1", env.SubscriptionID)
	assert.Nil(t, env.Count)
	assert.Equal(t, []int{1}, env.Filter.Kinds)
	assert.Equal(t, 10, env.Filter.Limit)
}

func TestCountEnvelopeFromJSONInvalidHyperLogLog(t *testing.T) {
	hll := strings.Repeat("zz", 256)

	env := &CountEnvelope{}
	require.Error(t, env.FromJSON(`["COUNT","sub1",{"count":7,"hll":"`+hll+`"}]`))
}

func TestCountEnvelopeFromJSONMissingFields(t *testing.T) {
	env := &CountEnvelope{}
	require.Error(t, env.FromJSON(`["COUNT","sub1"]`))
}

func TestCountEnvelopeMarshalCount(t *testing.T) {
	count := int64(42)
	env := CountEnvelope{SubscriptionID: "sub1", Count: &count}

	data, err := env.MarshalJSON()
	require.NoError(t, err)
	assert.JSONEq(t, `["COUNT","sub1",{"count":42}]`, string(data))
}

func TestCountEnvelopeMarshalCountWithHyperLogLog(t *testing.T) {
	count := int64(1)
	env := CountEnvelope{
		SubscriptionID: "sub1",
		Count:          &count,
		HyperLogLog:    bytes.Repeat([]byte{0xab}, 256),
	}

	data, err := env.MarshalJSON()
	require.NoError(t, err)
	assert.JSONEq(t, `["COUNT","sub1",{"count":1,"hll":"`+strings.Repeat("ab", 256)+`"}]`, string(data))
}

func TestCountEnvelopeMarshalFilter(t *testing.T) {
	env := CountEnvelope{
		SubscriptionID: "sub1",
		Filter:         Filter{Kinds: []int{1}, Limit: 10},
	}

	data, err := env.MarshalJSON()
	require.NoError(t, err)
	assert.JSONEq(t, `["COUNT","sub1",{"kinds":[1],"limit":10}]`, string(data))
}
