package main

import (
	"bufio"
	"flag"
	"fmt"
	"github.com/influxdb/influxdb/client"
	"net/url"
	"os"
	"reflect"
	"sflux/logger"
	"strconv"
	"strings"
	"time"
)

type Counters struct {
	Source             string
	IfIndex            int64
	IfType             int64
	IfSpeed            int64
	IfDirection        int64 // Derived from MAU MIB (RFC 2668) 0 = unknown, 1 = full-duplex, 2 = half-duplex, 3 = in, 4 = out
	IfStatus           int64
	IfInOctets         int64
	IfInUcastPkts      int64
	IfInMulticastPkts  int64
	IfInBroadcastPkts  int64
	IfInDiscards       int64
	IfInErrors         int64
	IfInUnknownProtos  int64
	IfOutOctets        int64
	IfOutUcastPkts     int64
	IfOutMulticastPkts int64
	IfOutBroadcastPkts int64
	IfOutDiscards      int64
	IfOutErrors        int64
	IfPromiscuousMode  int64
	TimeStamp          time.Time
}

var MetricFields = [...]string{"IfInOctets", "IfOutOctets", "IfInDiscards", "IfInBroadcastPkts", "IfInMulticastPkts", "IfInUcastPkts", "IfOutUcastPkts", "IfOutMulticastPkts", "IfOutBroadcastPkts", "IfOutDiscards", "IfOutErrors"}

var log logger.Log
var myCon *client.Client

var chunkSize int
var influxUser string
var influxPassword string
var influxPort int
var influxHost string
var influxDB string
var loglvl int

func getInt(cols []string, index int) int64 {
	res, _ := strconv.ParseInt(cols[index], 0, 64)
	return res
}

func main() {
	flag.IntVar(&chunkSize, "chunksize", 100, "Chunk size for counter packages written to database per batch, default=100")
	flag.IntVar(&influxPort, "p", 8086, "Port to connect to InfluxDB, default=8086")
	flag.StringVar(&influxUser, "u", "", "InfluxDB username")
	flag.StringVar(&influxPassword, "password", "", "InfluxDB password")
	flag.StringVar(&influxHost, "h", "localhost", "Host to connect to InfluxDB, default=localhost")
	flag.StringVar(&influxDB, "d", "", "Database to use")
	flag.IntVar(&loglvl, "loglevel", 2, "Desired loglevel (ERROR=0, WARN=1, INFO=2, DEBUG=3), default = 2")
	flag.Parse()
	log = logger.NewLog(loglvl)
	log.Info("loglevel: %d", loglvl)
	log.Info("chunksize: %d", chunkSize)
	log.Info("influxhost: %s", influxHost)
	log.Info("influxport: %d", influxPort)
	log.Info("influx database: %s", influxDB)
	run()
}

func run() {
	scanner := bufio.NewScanner(os.Stdin)
	all := scanN(scanner, chunkSize)
	for all != nil {
		insertIntoInflux(all)
		all = scanN(scanner, chunkSize)
	}
}

func scanN(scanner *bufio.Scanner, n int) []Counters {
	var all []Counters
	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		columns := strings.Split(line, ",")
		tsStr := strings.Split(columns[0], " ")[0]
		ts, _ := strconv.ParseInt(tsStr, 0, 64)
		timestamp := time.Unix(ts, 0)
		source := columns[1]
		columns = columns[2:]
		tmp := Counters{
			source,
			getInt(columns, 0),
			getInt(columns, 1),
			getInt(columns, 2),
			getInt(columns, 3),
			getInt(columns, 4),
			getInt(columns, 5),
			getInt(columns, 6),
			getInt(columns, 7),
			getInt(columns, 8),
			getInt(columns, 9),
			getInt(columns, 10),
			getInt(columns, 11),
			getInt(columns, 12),
			getInt(columns, 13),
			getInt(columns, 14),
			getInt(columns, 15),
			getInt(columns, 16),
			getInt(columns, 17),
			getInt(columns, 18),
			timestamp,
		}
		all = append(all, tmp)
		if len(all) == n {
			return all
		}
		count = count + 1
	}
	return all
}

func appendPoints(points []client.Point, counters []Counters, i int) {
	inst := counters[i]
	instVal := reflect.ValueOf(inst)
	log.Debug("process line: %d", i)
	for ii := range MetricFields {
		fieldName := MetricFields[ii]
		cVal := reflect.Indirect(instVal).FieldByName(fieldName).Int()
		log.Debug("CREATE POINT #%d, counter: %s source:%s ifindex:%d: value:%d", i*len(MetricFields)+ii, fieldName, inst.Source, inst.IfIndex, cVal)
		points[i*len(MetricFields)+ii] = client.Point{
			Measurement: fieldName,
			Tags: map[string]string{
				"Source":  inst.Source,
				"IfIndex": fmt.Sprintf("%d", inst.IfIndex),
			},
			Fields: map[string]interface{}{
				"value": cVal,
			},
			Time:      inst.TimeStamp,
			Precision: "s",
		}
	}
}

func insertIntoInflux(counters []Counters) {
	chunk := len(counters)
	log.Debug("allocate Point slice of size %d", (chunk * len(MetricFields)))
	pts := make([]client.Point, chunk*len(MetricFields))
	//var pts []client.Point
	for i := 0; i < chunk; i++ {
		appendPoints(pts, counters, i)
	}
	bps := client.BatchPoints{
		Points:          pts,
		Database:        influxDB,
		RetentionPolicy: "default",
	}
	con := getInfluxClient()
	log.Info("InfluxDB Client: start inserting chunk")
	_, err := con.Write(bps)
	if err != nil {
		log.Error(err)
	}
}

func getInfluxClient() client.Client {
	if myCon != nil {
		// Try to use cached/pooled connection
		dur, ver, err := myCon.Ping()
		if err == nil {
			log.Debug("Using pooled connection")
			log.Debug("InfluxDB Client: Happy as a hippo! Ping time: %v, Version: %s", dur, ver)
			return *myCon
		}
		log.Warn("Ping on pooled conn yielded err %s", err)
	}
	// Either there was no connection, or Ping yielded an error
	strUrl := fmt.Sprintf("http://%s:%d", influxHost, influxPort)
	u, err := url.Parse(strUrl)
	conf := client.Config{
		URL:      *u,
		Username: influxUser,
		Password: influxPassword,
	}
	log.Info("InfluxDB Client: Connecting to %s", strUrl)
	con, err := client.NewClient(conf)
	if err != nil {
		log.Error(err)
	}
	dur, ver, err := con.Ping()
	if err != nil {
		log.Error(err)
	}
	// Set con on state, so it can be reused later
	myCon = con
	log.Info("InfluxDB Client: Happy as a hippo! Ping time: %v, Version: %s", dur, ver)
	return *con
}
