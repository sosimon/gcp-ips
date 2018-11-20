// Retrieves a list of IP addresses used by each subnet in a shared VPC
// Formats results to Markdown tables and writes them to files
//
// See https://godoc.org/google.golang.org/api/compute/v1 and
// https://github.com/googleapis/google-api-go-client/tree/master/compute/v1/compute-gen.go
// for details on structures of AddressAggregatedList and InstanceAggregatedList

package main

import (
	"bytes"
	"log"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/olekukonko/tablewriter"
	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/compute/v1"
)

// A struct to hold the lists of addresses and instances for a particular project
// AddressList and InstanceList are the raw responses from GCP from calling
// service.Addresses.AggregatedList(project).Do() and
// service.Instances.AggregatedList(project).Do() respectively
type projectResources struct {
	Project      string
	AddressList  *compute.AddressAggregatedList
	InstanceList *compute.InstanceAggregatedList
}

// AddressInfo holds the fields that we care about in our output table
type AddressInfo struct {
	Project string
	IP      string
	Status  string
	Subnet  string
	User    string
}

// Initialize the Compute API client
func initClient() *compute.Service {
	ctx := context.Background()

	client, err := google.DefaultClient(ctx, compute.ComputeScope)
	if err != nil {
		log.Fatal(err)
	}

	computeService, err := compute.New(client)
	if err != nil {
		log.Fatal(err)
	}

	return computeService
}

// Get a list of service projects for a given host project
func getServiceProjects(hostProject string, service *compute.Service) (*compute.ProjectsGetXpnResources, error) {
	log.Printf("Looking for service projects in %s\n", hostProject)

	res, err := service.Projects.GetXpnResources(hostProject).Do()

	if err != nil {
		log.Printf("Error getting service projects for %s: %s", hostProject, err)
	}

	return res, err
}

// Get the AddressAggregatedList and InstanceAggregatedList for a particular project
func getResources(project string, service *compute.Service) *projectResources {
	log.Printf("Looking for instances and IPs in %s\n", project)

	addressAggregatedList, err := service.Addresses.AggregatedList(project).Do()

	if err != nil {
		log.Printf("Error getting reserved IPs for %s: %s", project, err)
	}

	instanceAggregatedList, err := service.Instances.AggregatedList(project).Do()
	if err != nil {
		log.Printf("Error getting instances for %s: %s", project, err)
	}

	output := &projectResources{
		Project:      project,
		AddressList:  addressAggregatedList,
		InstanceList: instanceAggregatedList,
	}

	return output
}

// Call getResources on all service projects attached to host project (shared VPC)
func getAllResources(hostProject string, service *compute.Service) []*projectResources {
	ch := make(chan *projectResources)
	var wg sync.WaitGroup

	// get list of service projects
	res, err := getServiceProjects(hostProject, service)
	if err != nil {
		log.Fatal(err)
	}

	// goroutine for each project to get list of reserved IPs
	for _, resource := range res.Resources {
		projectID := resource.Id
		wg.Add(1)
		go func(projectID string) {
			defer wg.Done()
			ch <- getResources(projectID, service)
		}(projectID)
	}

	// wait for all goroutines to finish and close the channel
	go func() {
		wg.Wait()
		close(ch)
	}()

	// gather all responses in output[]
	var output []*projectResources
	for s := range ch {
		if s != nil {
			output = append(output, s)
		}
	}

	return output
}

// Append an AddressInfo object into a map keyed by IP address
// Handle case where the entry already exists
func insertAddressInfo(addressInfoMap map[string]*AddressInfo, addressInfo *AddressInfo) {
	ip := addressInfo.IP
	// If IP already exists in the map, merge the information together. Existing entries has precedence.
	// If the new addressInfo struct has different values than the existing entry, it won't be captured.
	// Bottom line: this should work ok assuming the Address and Instances resources don't have
	// contradicting information. Mainly it's the subnet and user that could be different.
	if existingInfo, ok := addressInfoMap[ip]; ok {
		if existingInfo.Status == "" {
			existingInfo.Status = addressInfo.Status
		}
		if existingInfo.Subnet == "" {
			existingInfo.Subnet = addressInfo.Subnet
		}
		if existingInfo.User == "" {
			existingInfo.User = addressInfo.User
		}
	} else {
		addressInfoMap[ip] = addressInfo
	}
}

// Parse self-links to get just the resource name at the end
func getName(selfLink string) string {
	split := strings.Split(selfLink, "/")
	return split[len(split)-1]
}

// Process a list of projectResources, where each projectResource includes a list of all
// Address and Instance resources in the project.
// Returns a map of AddressInfo objects, whose keys are IP addresses
func flatten(projectResourceList []*projectResources) map[string]*AddressInfo {
	addressInfoMap := make(map[string]*AddressInfo)
	for _, p := range projectResourceList {
		if p.AddressList == nil {
			log.Printf(p.Project + " has no reserved addresses")
		} else {
			for _, addressScopedList := range p.AddressList.Items {
				if addressScopedList.Addresses != nil {
					for _, address := range addressScopedList.Addresses {
						// make sure user is not nil, which happens when reserved IP
						// is RESERVED but not IN_USE
						var user string
						if address.Users != nil {
							user = getName(address.Users[0])
						}
						insertAddressInfo(addressInfoMap, &AddressInfo{
							Project: p.Project,
							IP:      address.Address,
							Status:  address.Status,
							Subnet:  getName(address.Subnetwork),
							User:    user,
						})
					}
				}
			}
		}
		if p.InstanceList == nil {
			log.Printf(p.Project + " has no instances")
		} else {
			for _, instanceScopedList := range p.InstanceList.Items {
				if instanceScopedList.Instances != nil {
					for _, instance := range instanceScopedList.Instances {
						insertAddressInfo(addressInfoMap, &AddressInfo{
							Project: p.Project,
							IP:      instance.NetworkInterfaces[0].NetworkIP,
							Subnet:  getName(instance.NetworkInterfaces[0].Subnetwork),
							User:    instance.Name,
						})
					}
				}
			}
		}
	}
	return addressInfoMap
}

// Process a list of projectResources and re-organize it by subnet
func extractFields(projectResourceList []*projectResources) map[string][]*AddressInfo {
	addressInfoBySubnet := make(map[string][]*AddressInfo)
	addressInfoByIP := flatten(projectResourceList)
	for _, addressInfo := range addressInfoByIP {
		subnet := addressInfo.Subnet
		addressInfoBySubnet[subnet] = append(addressInfoBySubnet[subnet], addressInfo)
	}
	return addressInfoBySubnet
}

// Given a particular subnet and its list of AddressInfo objects,
// Sort by IP address and then format and write info to a Markdown table
func writeToFile(subnet string, addressInfoList []*AddressInfo) {
	var data [][]string

	// Create file
	f, err := os.Create(subnet + ".md")
	defer f.Close()
	if err != nil {
		log.Fatal(err)
	}

	// Write header
	_, err = f.WriteString("# Reserved IPs for " + subnet + "\n")
	if err != nil {
		log.Fatal(err)
	}

	// Sort IPs in ascending order (properly)
	sort.Slice(addressInfoList, func(i, j int) bool {
		a := net.ParseIP(addressInfoList[i].IP)
		b := net.ParseIP(addressInfoList[j].IP)
		return bytes.Compare(a, b) < 0
	})

	for _, addressInfo := range addressInfoList {
		// Append data to be written to file
		data = append(data, []string{
			addressInfo.IP,
			addressInfo.Project,
			addressInfo.Status,
			addressInfo.User,
		})
	}

	// Write data to file
	table := tablewriter.NewWriter(f)
	table.SetHeader([]string{"IP", "GCP Project", "Status", "User"})
	table.SetBorders(tablewriter.Border{Left: true, Top: false, Right: true, Bottom: false})
	table.SetCenterSeparator("|")
	table.AppendBulk(data)
	table.Render()

	log.Printf("Writing to " + subnet + ".md\n")
}

// Format and write all addresses to Markdown files
// Loop through addressBySubnet map,
// call writeToFile for each subnet,
// with each subnet in a different file
func writeAll(addressesBySubnet map[string][]*AddressInfo) {
	for subnet, addressInfoList := range addressesBySubnet {
		if subnet != "" {
			writeToFile(subnet, addressInfoList)
		}
	}
}

func main() {
	start := time.Now()

	if len(os.Args) < 2 {
		log.Fatalln("Missing required parameter: host-project")
	}

	hostProject := os.Args[1]

	computeService := initClient()
	resources := getAllResources(hostProject, computeService)
	addressInfoBySubnet := extractFields(resources)
	writeAll(addressInfoBySubnet)

	elapsed := time.Since(start)
	log.Printf("Took %.2f seconds", elapsed.Seconds())
}
