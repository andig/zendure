package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/coder/websocket"
	_ "github.com/joho/godotenv/autoload"
	"github.com/samber/lo"
)

const (
	MinCharge       = 100  // minimum charge power
	MaxCharge       = 1200 // maximum charge power
	ChargeMargin    = 0
	MinDischarge    = 30  // minimum discharge power
	MaxDischarge    = 800 // maximum discharge power
	DischargeMargin = 50
)

const (
	AC_MODE      = "acMode"
	INPUT_LIMIT  = "setInputLimit"
	OUTPUT_LIMIT = "setOutputLimit"

	MODE_INPUT  = 1
	MODE_OUTPUT = 2

	MAX_INT = int64(math.MaxInt64)
)

var (
	EvccURI     = strings.TrimSuffix(strings.TrimPrefix(getenv("EVCC_URI", "evcc:7070"), "http://"), "/")
	WsURI       = "ws://" + EvccURI + "/ws"
	StateURI    = "http://" + EvccURI + "/api/state"
	IoBrokerURI = "http://" + strings.TrimSuffix(strings.TrimPrefix(getenv("IOBROKER_URI", "iobroker:8087"), "http://"), "/")
	// Instance       = "zendure-solarflow.0"
	InstanceDevice = getenv("IOBROKER_DEVICE", "zendure-solarflow.0.gDa3tb.wywWpySH")
)

type WsData struct {
	Grid *struct {
		Power float64
	}
}

func getenv(key string, def ...string) string {
	res := strings.TrimSpace(os.Getenv(key))
	if res == "" {
		if len(def) == 1 {
			return def[0]
		}

		log.Fatalln("missing", key)
	}
	return res
}

// func status() error {
// 	uri := fmt.Sprintf("%s/states?pattern=%s", IoBrokerURI, Instance+".connected")
// 	resp, err := http.Get(uri)
// 	if err != nil {
// 		return err
// 	}
// 	var res struct {
// 		Val bool
// 	}
// 	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
// 		return err
// 	}
// 	if !res.Val {
// 		return errors.New("device not connected")
// 	}
// 	return nil
// }

func gridPower() (int64, error) {
	var state struct {
		Result struct {
			Grid struct {
				Power float64
			}
		}
	}

	resp, err := http.Get(StateURI)
	if err == nil {
		err = json.NewDecoder(resp.Body).Decode(&state)
	}

	return int64(state.Result.Grid.Power), err
}

// func getSensor(sensor string) (int64, error) {
// 	uri := fmt.Sprintf("%s/getPlainValue/%s.%s", IoBrokerURI, InstanceDevice, sensor)
// 	resp, err := http.Get(uri)
// 	if err != nil {
// 		return 0, err
// 	}

// 	b, err := io.ReadAll(resp.Body)
// 	if err != nil {
// 		return 0, err
// 	}

// 	return strconv.ParseInt(string(b), 10, 64)
// }

type value struct {
	ID  string
	Val int64
}

type sensors []value

func (s sensors) value(sensor string) int64 {
	sensor = fmt.Sprintf("%s.%s", InstanceDevice, sensor)

	for _, v := range s {
		if v.ID == sensor {
			return v.Val
		}
	}
	return 0
}

func getSensors(sensor ...string) (sensors, error) {
	uri := fmt.Sprintf("%s/getBulk/%s", IoBrokerURI, strings.Join(lo.Map(sensor, func(sensor string, _ int) string {
		return fmt.Sprintf("%s.%s", InstanceDevice, sensor)
	}), ","))
	resp, err := http.Get(uri)
	if err != nil {
		return nil, err
	}

	var res sensors
	err = json.NewDecoder(resp.Body).Decode(&res)

	return res, err
}

func setControl(cmd string, value int64) {
	log.Println(cmd+":", value)

	uri := fmt.Sprintf("%s/set/%s.control.%s?value=%d", IoBrokerURI, InstanceDevice, cmd, value)
	if _, err := http.Get(uri); err != nil {
		log.Println(uri, ":", err)
	}
}

func control(gridC <-chan float64) {
	var battery, grid *int64

	for {
		select {
		case gridFloat := <-gridC:
			grid = lo.ToPtr(int64(gridFloat))

		case <-time.After(30 * time.Second):
			gridInt, err := gridPower()
			if err != nil {
				log.Println(err)
				continue
			}
			grid = &gridInt
		}

		sensors, err := getSensors("minSoc", "acMode", "gridInputPower", "outputHomePower", "electricLevel")
		if err != nil {
			log.Println(err)
			continue
		}

		minSoc := sensors.value("minSoc")
		mode := sensors.value("acMode")
		in := sensors.value("gridInputPower")
		out := sensors.value("outputHomePower")
		soc := sensors.value("electricLevel")

		new := *grid - in + out
		log.Println(new, "=", *grid, "grid -", in, "in +", out, "out")

		if new >= 0 {
			// DISCHARGE
			new = max(0, min(new-DischargeMargin, MaxDischarge))

			if soc <= minSoc || new < MinDischarge {
				new = 0
			}

			if battery == nil || new != *battery {
				if mode != MODE_OUTPUT {
					setControl(INPUT_LIMIT, 0)
					setControl(AC_MODE, MODE_OUTPUT)
				}

				setControl(OUTPUT_LIMIT, new)
			}

			if soc <= minSoc {
				log.Printf("battery low: %d%%", soc)
				time.Sleep(time.Minute)
			}
		} else {
			// CHARGE
			new = min(0, max(new+ChargeMargin, -MaxCharge))

			if new > -MinCharge {
				new = 0
			}

			if battery == nil || new != *battery {
				if mode != MODE_INPUT {
					setControl(OUTPUT_LIMIT, 0)
					setControl(AC_MODE, MODE_INPUT)
				}

				setControl(INPUT_LIMIT, -new)
			}
		}

		battery = &new
	}
}

func ws(gridC chan<- float64) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		conn, _, err := websocket.Dial(ctx, WsURI, nil)
		cancel()

		if err != nil {
			log.Println("websocket dial:", err)
			time.Sleep(10 * time.Second)
			continue
		}

		for {
			_, r, err := conn.Reader(context.Background())
			if err != nil {
				log.Println("websocket read:", err)
				_ = conn.Close(websocket.StatusAbnormalClosure, "done")
				break
			}

			var res WsData
			if err := json.NewDecoder(r).Decode(&res); err != nil {
				log.Println("websocket decode:", err)
				_ = conn.Close(websocket.StatusAbnormalClosure, "done")
				break
			}

			if res.Grid != nil {
				gridC <- res.Grid.Power
			}
		}
	}
}

func main() {
	fmt.Println("evcc state:", StateURI)
	fmt.Println("evcc ws:", WsURI)
	fmt.Println("iobroker:", IoBrokerURI)

	gridC := make(chan float64, 1)

	go ws(gridC)
	go control(gridC)

	c := make(chan os.Signal, 1)
	done := make(chan bool, 1)

	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		close(done)
	}()

	<-done
}
