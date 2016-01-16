/*
Licensed to the Apache Software Foundation (ASF) under one
or more contributor license agreements.  See the NOTICE file
distributed with this work for additional information
regarding copyright ownership.  The ASF licenses this file
to you under the Apache License, Version 2.0 (the
"License"); you may not use this file except in compliance
with the License.  You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing,
software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
KIND, either express or implied.  See the License for the
specific language governing permissions and limitations
under the License.
*/

package obcpbft

import (
	gp "google/protobuf"
	"math/rand"
	"reflect"
	"testing"

	"github.com/golang/protobuf/proto"
	"github.com/google/gofuzz"
	"github.com/op/go-logging"

	"fmt"

	pb "github.com/openblockchain/obc-peer/protos"
)

func TestFuzz(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping fuzz test")
	}

	logging.SetBackend(logging.InitForTesting(logging.ERROR))

	primary := newPbftCore(0, readConfig(), newMock())
	defer primary.close()
	backup := newPbftCore(1, readConfig(), newMock())
	defer backup.close()

	f := fuzz.New()

	for i := 0; i < 30; i++ {
		msg := &Message{}
		f.Fuzz(msg)
		// roundtrip through protobufs to translate
		// nil slices into empty slices
		raw, _ := proto.Marshal(msg)
		proto.Unmarshal(raw, msg)
		primary.recvMsgSync(msg)
		backup.recvMsgSync(msg)
	}

	logging.Reset()
}

func (msg *Message) Fuzz(c fuzz.Continue) {
	switch c.RandUint64() % 7 {
	case 0:
		m := &Message_Request{}
		c.Fuzz(m)
		msg.Payload = m
	case 1:
		m := &Message_PrePrepare{}
		c.Fuzz(m)
		msg.Payload = m
	case 2:
		m := &Message_Prepare{}
		c.Fuzz(m)
		msg.Payload = m
	case 3:
		m := &Message_Commit{}
		c.Fuzz(m)
		msg.Payload = m
	case 4:
		m := &Message_Checkpoint{}
		c.Fuzz(m)
		msg.Payload = m
	case 5:
		m := &Message_ViewChange{}
		c.Fuzz(m)
		msg.Payload = m
	case 6:
		m := &Message_NewView{}
		c.Fuzz(m)
		msg.Payload = m
	}
}

func TestMinimalFuzz(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping fuzz test")
	}

	net := makeTestnet(1, makeTestnetPbftCore)
	defer net.close()
	fuzzer := &protoFuzzer{r: rand.New(rand.NewSource(0))}
	net.filterFn = fuzzer.fuzzPacket

	noExec := 0
	for reqid := 1; reqid < 30; reqid++ {
		if reqid%3 == 0 {
			fuzzer.fuzzNode = fuzzer.r.Intn(len(net.replicas))
			fmt.Printf("Fuzzing node %d\n", fuzzer.fuzzNode)
		}

		// Create a message of type: `OpenchainMessage_CHAIN_TRANSACTION`
		txTime := &gp.Timestamp{Seconds: int64(reqid), Nanos: 0}
		tx := &pb.Transaction{Type: pb.Transaction_CHAINCODE_NEW, Timestamp: txTime}
		txPacked, err := proto.Marshal(tx)
		if err != nil {
			t.Fatalf("Failed to marshal TX block: %s", err)
		}
		msg := &Message{&Message_Request{&Request{Payload: txPacked}}}
		for _, inst := range net.replicas {
			inst.pbft.recvMsgSync(msg)
		}
		if err != nil {
			t.Fatalf("Request failed: %s", err)
		}

		err = net.process()
		if err != nil {
			t.Fatalf("Processing failed: %s", err)
		}

		quorum := 0
		for _, r := range net.replicas {
			if len(r.executed) > 0 {
				quorum++
				r.executed = nil
			}
		}
		if quorum < len(net.replicas)/3 {
			noExec++
		}
		if noExec > 1 {
			noExec = 0
			for _, r := range net.replicas {
				r.pbft.sendViewChange()
			}
			err = net.process()
			if err != nil {
				t.Fatalf("Processing failed: %s", err)
			}
		}
	}
}

type protoFuzzer struct {
	fuzzNode int
	r        *rand.Rand
}

func (f *protoFuzzer) fuzzPacket(src int, dst int, msgOuter []byte) []byte {
	if dst != -1 || src != f.fuzzNode {
		return msgOuter
	}

	// XXX only with some probability
	msg := &Message{}
	if proto.Unmarshal(msgOuter, msg) != nil {
		panic("could not unmarshal")
	}

	fmt.Printf("Will fuzz %v\n", msg)

	if m := msg.GetPrePrepare(); m != nil {
		f.fuzzPayload(m)
	}
	if m := msg.GetPrepare(); m != nil {
		f.fuzzPayload(m)
	}
	if m := msg.GetCommit(); m != nil {
		f.fuzzPayload(m)
	}
	if m := msg.GetCheckpoint(); m != nil {
		f.fuzzPayload(m)
	}
	if m := msg.GetViewChange(); m != nil {
		f.fuzzPayload(m)
	}
	if m := msg.GetNewView(); m != nil {
		f.fuzzPayload(m)
	}

	newMsg, _ := proto.Marshal(msg)
	return newMsg
}

func (f *protoFuzzer) fuzzPayload(s interface{}) {
	v := reflect.ValueOf(s).Elem()
	t := v.Type()

	var elems []reflect.Value
	var fields []string
	for i := 0; i < v.NumField(); i++ {
		if t.Field(i).Name == "ReplicaId" {
			continue
		}
		elems = append(elems, v.Field(i))
		fields = append(fields, t.Field(i).Name)
	}

	idx := f.r.Intn(len(elems))
	elm := elems[idx]
	fld := fields[idx]
	fmt.Printf("Fuzzing %s:%v\n", fld, elm)
	f.Fuzz(elm)
}

func (f *protoFuzzer) Fuzz(v reflect.Value) {
	if !v.CanSet() {
		return
	}

	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f.FuzzInt(v)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f.FuzzUint(v)
	case reflect.String:
		str := ""
		for i := 0; i < v.Len(); i++ {
			str = str + string(' '+rune(f.r.Intn(94)))
		}
		v.SetString(str)
		return
	case reflect.Ptr:
		if !v.IsNil() {
			f.Fuzz(v.Elem())
		}
		return
	case reflect.Slice:
		mode := f.r.Intn(3)
		switch {
		case v.Len() > 0 && mode == 0:
			// fuzz entry
			f.Fuzz(v.Index(f.r.Intn(v.Len())))
		case v.Len() > 0 && mode == 1:
			// remove entry
			entry := f.r.Intn(v.Len())
			pre := v.Slice(0, entry)
			post := v.Slice(entry+1, v.Len())
			v.Set(reflect.AppendSlice(pre, post))
		default:
			// add entry
			entry := reflect.MakeSlice(v.Type(), 1, 1)
			f.Fuzz(entry) // XXX fill all fields
			v.Set(reflect.AppendSlice(v, entry))
		}
		return
	case reflect.Struct:
		f.Fuzz(v.Field(f.r.Intn(v.NumField())))
		return
	case reflect.Map:
		// TODO fuzz map
	default:
		panic(fmt.Sprintf("Not fuzzing %v %+v", v.Kind(), v))
	}
}

func (f *protoFuzzer) FuzzInt(v reflect.Value) {
	v.SetInt(v.Int() + f.fuzzyInt())
}

func (f *protoFuzzer) FuzzUint(v reflect.Value) {
	val := v.Uint()
	for {
		delta := f.fuzzyInt()
		if delta > 0 || uint64(-delta) < val {
			v.SetUint(val + uint64(delta))
			return
		}
	}
}

func (f *protoFuzzer) fuzzyInt() int64 {
	i := int64(rand.NewZipf(f.r, 3, 1, 200).Uint64() + 1)
	if rand.Intn(2) == 0 {
		i = -i
	}
	fmt.Printf("Changing int by %d\n", i)
	return i
}

func (f *protoFuzzer) FuzzSlice(v reflect.Value) {
}