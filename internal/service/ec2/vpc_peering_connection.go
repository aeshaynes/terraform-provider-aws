package ec2

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/hashicorp/aws-sdk-go-base/v2/awsv1shim/v2/tfawserr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-aws/internal/conns"
	tftags "github.com/hashicorp/terraform-provider-aws/internal/tags"
	"github.com/hashicorp/terraform-provider-aws/internal/tfresource"
	"github.com/hashicorp/terraform-provider-aws/internal/verify"
)

func ResourceVPCPeeringConnection() *schema.Resource {
	return &schema.Resource{
		Create: resourceVPCPeeringConnectionCreate,
		Read:   resourceVPCPeeringConnectionRead,
		Update: resourceVPCPeeringConnectionUpdate,
		Delete: resourceVPCPeeringConnectionDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(1 * time.Minute),
			Update: schema.DefaultTimeout(1 * time.Minute),
			Delete: schema.DefaultTimeout(1 * time.Minute),
		},

		Schema: map[string]*schema.Schema{
			"accept_status": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"accepter": vpcPeeringConnectionOptionsSchema,
			"auto_accept": {
				Type:     schema.TypeBool,
				Optional: true,
			},
			"peer_owner_id": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Computed: true,
			},
			"peer_region": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Computed: true,
			},
			"peer_vpc_id": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"requester": vpcPeeringConnectionOptionsSchema,
			"tags":      tftags.TagsSchema(),
			"tags_all":  tftags.TagsSchemaComputed(),
			"vpc_id": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
		},

		CustomizeDiff: verify.SetTagsDiff,
	}
}

var vpcPeeringConnectionOptionsSchema = &schema.Schema{
	Type:     schema.TypeList,
	Optional: true,
	Computed: true,
	MaxItems: 1,
	Elem: &schema.Resource{
		Schema: map[string]*schema.Schema{
			"allow_classic_link_to_remote_vpc": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"allow_remote_vpc_dns_resolution": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"allow_vpc_to_remote_classic_link": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
		},
	},
}

func resourceVPCPeeringConnectionCreate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn
	defaultTagsConfig := meta.(*conns.AWSClient).DefaultTagsConfig
	tags := defaultTagsConfig.MergeTags(tftags.New(d.Get("tags").(map[string]interface{})))

	input := &ec2.CreateVpcPeeringConnectionInput{
		PeerVpcId:         aws.String(d.Get("peer_vpc_id").(string)),
		TagSpecifications: ec2TagSpecificationsFromKeyValueTags(tags, ec2.ResourceTypeVpcPeeringConnection),
		VpcId:             aws.String(d.Get("vpc_id").(string)),
	}

	if v, ok := d.GetOk("peer_owner_id"); ok {
		input.PeerOwnerId = aws.String(v.(string))
	}

	if v, ok := d.GetOk("peer_region"); ok {
		if _, ok := d.GetOk("auto_accept"); ok {
			return fmt.Errorf("`peer_region` cannot be set whilst `auto_accept` is `true` when creating an EC2 VPC Peering Connection")
		}

		input.PeerRegion = aws.String(v.(string))
	}

	log.Printf("[DEBUG] Creating EC2 VPC Peering Connection: %s", input)
	output, err := conn.CreateVpcPeeringConnection(input)

	if err != nil {
		return fmt.Errorf("error creating EC2 VPC Peering Connection: %w", err)
	}

	d.SetId(aws.StringValue(output.VpcPeeringConnection.VpcPeeringConnectionId))

	if _, err := WaitVPCPeeringConnectionActive(conn, d.Id(), d.Timeout(schema.TimeoutCreate)); err != nil {
		return fmt.Errorf("error waiting for EC2 VPC Peering Connection (%s) create: %w", d.Id(), err)
	}

	return resourceVPCPeeringConnectionUpdate(d, meta)
}

func resourceVPCPeeringConnectionRead(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn
	defaultTagsConfig := meta.(*conns.AWSClient).DefaultTagsConfig
	ignoreTagsConfig := meta.(*conns.AWSClient).IgnoreTagsConfig

	pc, err := FindVPCPeeringConnectionByID(conn, d.Id())

	if !d.IsNewResource() && tfresource.NotFound(err) {
		log.Printf("[WARN] EC2 VPC Peering Connection %s not found, removing from state", d.Id())
		d.SetId("")
		return nil
	}

	if err != nil {
		return fmt.Errorf("error reading EC2 VPC Peering Connection (%s): %w", d.Id(), err)
	}

	d.Set("accept_status", pc.Status.Code)
	d.Set("peer_region", pc.AccepterVpcInfo.Region)

	if accountID := meta.(*conns.AWSClient).AccountID; accountID == aws.StringValue(pc.AccepterVpcInfo.OwnerId) && accountID != aws.StringValue(pc.RequesterVpcInfo.OwnerId) {
		// We're the accepter.
		d.Set("peer_owner_id", pc.RequesterVpcInfo.OwnerId)
		d.Set("peer_vpc_id", pc.RequesterVpcInfo.VpcId)
		d.Set("vpc_id", pc.AccepterVpcInfo.VpcId)
	} else {
		// We're the requester.
		d.Set("peer_owner_id", pc.AccepterVpcInfo.OwnerId)
		d.Set("peer_vpc_id", pc.AccepterVpcInfo.VpcId)
		d.Set("vpc_id", pc.RequesterVpcInfo.VpcId)
	}

	if pc.AccepterVpcInfo.PeeringOptions != nil {
		if err := d.Set("accepter", []interface{}{flattenVpcPeeringConnectionOptionsDescription(pc.AccepterVpcInfo.PeeringOptions)}); err != nil {
			return fmt.Errorf("error setting accepter: %w", err)
		}
	} else {
		d.Set("accepter", nil)
	}

	if pc.RequesterVpcInfo.PeeringOptions != nil {
		if err := d.Set("requester", []interface{}{flattenVpcPeeringConnectionOptionsDescription(pc.RequesterVpcInfo.PeeringOptions)}); err != nil {
			return fmt.Errorf("error setting requester: %w", err)
		}
	} else {
		d.Set("requester", nil)
	}

	tags := KeyValueTags(pc.Tags).IgnoreAWS().IgnoreConfig(ignoreTagsConfig)

	//lintignore:AWSR002
	if err := d.Set("tags", tags.RemoveDefaultConfig(defaultTagsConfig).Map()); err != nil {
		return fmt.Errorf("error setting tags: %w", err)
	}

	if err := d.Set("tags_all", tags.Map()); err != nil {
		return fmt.Errorf("error setting tags_all: %w", err)
	}

	return nil
}

func resourceVPCPeeringConnectionUpdate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn

	if d.HasChange("tags_all") && !d.IsNewResource() {
		o, n := d.GetChange("tags_all")

		if err := UpdateTags(conn, d.Id(), o, n); err != nil {
			return fmt.Errorf("error updating EC2 VPC Peering Connection (%s) tags: %s", d.Id(), err)
		}
	}

	pcRaw, statusCode, err := vpcPeeringConnectionRefreshState(conn, d.Id())()
	if err != nil {
		return fmt.Errorf("Error reading VPC Peering Connection: %s", err)
	}

	if pcRaw == nil {
		log.Printf("[WARN] VPC Peering Connection (%s) not found, removing from state", d.Id())
		d.SetId("")
		return nil
	}

	if _, ok := d.GetOk("auto_accept"); ok && statusCode == ec2.VpcPeeringConnectionStateReasonCodePendingAcceptance {
		statusCode, err = resourceVPCPeeringConnectionAccept(conn, d.Id())
		if err != nil {
			return fmt.Errorf("Unable to accept VPC Peering Connection: %s", err)
		}
		log.Printf("[DEBUG] VPC Peering Connection accept status: %s", statusCode)

		// "OperationNotPermitted: Peering pcx-0000000000000000 is not active. Peering options can be added only to active peerings."
		if err := vpcPeeringConnectionWaitUntilAvailable(conn, d.Id(), d.Timeout(schema.TimeoutUpdate)); err != nil {
			return fmt.Errorf("Error waiting for VPC Peering Connection to become available: %s", err)
		}
	}

	if d.HasChanges("accepter", "requester") {
		if statusCode == ec2.VpcPeeringConnectionStateReasonCodeActive || statusCode == ec2.VpcPeeringConnectionStateReasonCodeProvisioning {
			pc := pcRaw.(*ec2.VpcPeeringConnection)
			crossRegionPeering := aws.StringValue(pc.RequesterVpcInfo.Region) != aws.StringValue(pc.AccepterVpcInfo.Region)

			req := &ec2.ModifyVpcPeeringConnectionOptionsInput{
				VpcPeeringConnectionId: aws.String(d.Id()),
			}
			if d.HasChange("accepter") {
				req.AccepterPeeringConnectionOptions = expandVPCPeeringConnectionOptions(d.Get("accepter").([]interface{}), crossRegionPeering)
			}
			if d.HasChange("requester") {
				req.RequesterPeeringConnectionOptions = expandVPCPeeringConnectionOptions(d.Get("requester").([]interface{}), crossRegionPeering)
			}

			log.Printf("[DEBUG] Modifying VPC Peering Connection options: %s", req)
			if _, err := conn.ModifyVpcPeeringConnectionOptions(req); err != nil {
				return fmt.Errorf("error modifying VPC Peering Connection (%s) Options: %s", d.Id(), err)
			}
		} else {
			return fmt.Errorf("Unable to modify peering options. The VPC Peering Connection "+
				"%q is not active. Please set `auto_accept` attribute to `true`, "+
				"or activate VPC Peering Connection manually.", d.Id())
		}
	}

	return resourceVPCPeeringConnectionRead(d, meta)
}

func resourceVPCPeeringConnectionDelete(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn

	log.Printf("[INFO] Deleting EC2 VPC Peering Connection: %s", d.Id())
	_, err := conn.DeleteVpcPeeringConnection(&ec2.DeleteVpcPeeringConnectionInput{
		VpcPeeringConnectionId: aws.String(d.Id()),
	})

	if tfawserr.ErrCodeEquals(err, ErrCodeInvalidVpcPeeringConnectionIDNotFound) {
		return nil
	}

	// "InvalidStateTransition: Invalid state transition for pcx-0000000000000000, attempted to transition from failed to deleting"
	if tfawserr.ErrMessageContains(err, "InvalidStateTransition", "to deleting") {
		return nil
	}

	if err != nil {
		return fmt.Errorf("error deleting EC2 VPC Peering Connection (%s): %w", d.Id(), err)
	}

	if _, err := WaitVPCPeeringConnectionDeleted(conn, d.Id(), d.Timeout(schema.TimeoutDelete)); err != nil {
		return fmt.Errorf("error waiting for EC2 VPC Peering Connection (%s) delete: %s", d.Id(), err)
	}

	return nil
}

func resourceVPCPeeringConnectionAccept(conn *ec2.EC2, id string) (string, error) {
	log.Printf("[INFO] Accept VPC Peering Connection with ID: %s", id)

	req := &ec2.AcceptVpcPeeringConnectionInput{
		VpcPeeringConnectionId: aws.String(id),
	}

	resp, err := conn.AcceptVpcPeeringConnection(req)
	if err != nil {
		return "", err
	}

	return aws.StringValue(resp.VpcPeeringConnection.Status.Code), nil
}

// vpcPeeringConnection returns the VPC peering connection corresponding to the specified identifier.
// Returns nil if no VPC peering connection is found or the connection has reached a terminal state
// according to https://docs.aws.amazon.com/vpc/latest/peering/vpc-peering-basics.html#vpc-peering-lifecycle.
func vpcPeeringConnection(conn *ec2.EC2, vpcPeeringConnectionID string) (*ec2.VpcPeeringConnection, error) {
	outputRaw, _, err := StatusVPCPeeringConnection(conn, vpcPeeringConnectionID)()

	if output, ok := outputRaw.(*ec2.VpcPeeringConnection); ok {
		return output, err
	}

	return nil, err
}

func vpcPeeringConnectionRefreshState(conn *ec2.EC2, id string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		resp, err := conn.DescribeVpcPeeringConnections(&ec2.DescribeVpcPeeringConnectionsInput{
			VpcPeeringConnectionIds: aws.StringSlice([]string{id}),
		})
		if err != nil {
			if tfawserr.ErrMessageContains(err, "InvalidVpcPeeringConnectionID.NotFound", "") {
				return nil, ec2.VpcPeeringConnectionStateReasonCodeDeleted, nil
			}

			return nil, "", err
		}

		if resp == nil || resp.VpcPeeringConnections == nil ||
			len(resp.VpcPeeringConnections) == 0 || resp.VpcPeeringConnections[0] == nil {
			// Sometimes AWS just has consistency issues and doesn't see
			// our peering connection yet. Return an empty state.
			return nil, "", nil
		}
		pc := resp.VpcPeeringConnections[0]
		if pc.Status == nil {
			// Sometimes AWS just has consistency issues and doesn't see
			// our peering connection yet. Return an empty state.
			return nil, "", nil
		}
		statusCode := aws.StringValue(pc.Status.Code)

		// A VPC Peering Connection can exist in a failed state due to
		// incorrect VPC ID, account ID, or overlapping IP address range,
		// thus we short circuit before the time out would occur.
		if statusCode == ec2.VpcPeeringConnectionStateReasonCodeFailed {
			return nil, statusCode, errors.New(aws.StringValue(pc.Status.Message))
		}

		return pc, statusCode, nil
	}
}

func vpcPeeringConnectionWaitUntilAvailable(conn *ec2.EC2, id string, timeout time.Duration) error {
	// Wait for the vpc peering connection to become available
	log.Printf("[DEBUG] Waiting for VPC Peering Connection (%s) to become available.", id)
	stateConf := &resource.StateChangeConf{
		Pending: []string{
			ec2.VpcPeeringConnectionStateReasonCodeInitiatingRequest,
			ec2.VpcPeeringConnectionStateReasonCodeProvisioning,
		},
		Target: []string{
			ec2.VpcPeeringConnectionStateReasonCodePendingAcceptance,
			ec2.VpcPeeringConnectionStateReasonCodeActive,
		},
		Refresh: vpcPeeringConnectionRefreshState(conn, id),
		Timeout: timeout,
	}
	if _, err := stateConf.WaitForState(); err != nil {
		return fmt.Errorf("Error waiting for VPC Peering Connection (%s) to become available: %s", id, err)
	}
	return nil
}

func flattenVpcPeeringConnectionOptionsDescription(apiObject *ec2.VpcPeeringConnectionOptionsDescription) map[string]interface{} {
	if apiObject == nil {
		return nil
	}

	tfMap := map[string]interface{}{}

	if v := apiObject.AllowDnsResolutionFromRemoteVpc; v != nil {
		tfMap["allow_remote_vpc_dns_resolution"] = aws.BoolValue(v)
	}

	if v := apiObject.AllowEgressFromLocalClassicLinkToRemoteVpc; v != nil {
		tfMap["allow_classic_link_to_remote_vpc"] = aws.BoolValue(v)
	}

	if v := apiObject.AllowEgressFromLocalVpcToRemoteClassicLink; v != nil {
		tfMap["allow_vpc_to_remote_classic_link"] = aws.BoolValue(v)
	}

	return tfMap
}
