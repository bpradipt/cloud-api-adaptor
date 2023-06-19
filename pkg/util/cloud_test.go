package util

import (
	"testing"
)

func TestVerifyCloudInstanceType(t *testing.T) {
	type args struct {
		instanceType        string
		instanceTypes       []string
		defaultInstanceType string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		// Add test case with instanceType="t2.small", instanceTypes=["t2.small, t2.medium"], defaultInstanceType="t2.medium"
		{
			name: "instanceType=t2.small, instanceTypes=[t2.small, t2.medium], defaultInstanceType=t2.medium",
			args: args{
				instanceType:        "t2.small",
				instanceTypes:       []string{"t2.small", "t2.medium"},
				defaultInstanceType: "t2.medium",
			},
			want:    "t2.small",
			wantErr: false,
		},
		// Add test case with instanceType="t2.small", instanceTypes=["t2.medium"], defaultInstanceType="t2.medium"
		{
			name: "instanceType=t2.small, instanceTypes=[t2.medium], defaultInstanceType=t2.medium",
			args: args{
				instanceType:        "t2.small",
				instanceTypes:       []string{"t2.medium"},
				defaultInstanceType: "t2.medium",
			},
			want:    "",
			wantErr: true,
		},
		// Add test case with instanceType="", instanceTypes=["t2.medium"], defaultInstanceType="t2.medium"
		{
			name: "instanceType=, instanceTypes=[t2.medium], defaultInstanceType=t2.medium",
			args: args{
				instanceType:        "",
				instanceTypes:       []string{"t2.medium"},
				defaultInstanceType: "t2.medium",
			},
			want:    "t2.medium",
			wantErr: false,
		},
		// Add test case with instanceType="", instanceTypes=[], defaultInstanceType="t2.medium"
		{
			name: "instanceType=, instanceTypes=[], defaultInstanceType=t2.medium",
			args: args{
				instanceType:        "",
				instanceTypes:       []string{},
				defaultInstanceType: "t2.medium",
			},
			want:    "t2.medium",
			wantErr: false,
		},
		// Add test case with instanceType="t2.small", instanceTypes=[], defaultInstanceType="t2.medium"
		{
			name: "instanceType=t2.small, instanceTypes=[], defaultInstanceType=t2.medium",
			args: args{
				instanceType:        "t2.small",
				instanceTypes:       []string{},
				defaultInstanceType: "t2.medium",
			},
			want:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VerifyCloudInstanceType(tt.args.instanceType, tt.args.instanceTypes, tt.args.defaultInstanceType)

			if (err != nil) != tt.wantErr {
				t.Errorf("VerifyCloudInstanceType() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("VerifyCloudInstanceType() = %v, want %v", got, tt.want)
			}

		})
	}
}
