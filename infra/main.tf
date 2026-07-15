terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = "ap-south-1" 
}

# Dynamically look up the absolute latest official Ubuntu 24.04 LTS AMI
data "aws_ami" "latest_ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical's verified AWS Owner ID

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }

  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

# Network Topology Layout
resource "aws_vpc" "platform_vpc" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_hostnames = true
  tags = { Name = "platform-engineering-vpc" }
}

resource "aws_subnet" "public_subnet" {
  vpc_id                  = aws_vpc.platform_vpc.id
  cidr_block              = "10.0.1.0/24"
  map_public_ip_on_launch = true
  availability_zone       = "ap-south-1a"
  tags = { Name = "platform-public-subnet" }
}

resource "aws_internet_gateway" "igw" {
  vpc_id = aws_vpc.platform_vpc.id
  tags   = { Name = "platform-gateway" }
}

resource "aws_route_table" "public_rt" {
  vpc_id = aws_vpc.platform_vpc.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.igw.id
  }
  tags = { Name = "platform-public-rt" }
}

resource "aws_route_table_association" "public_assoc" {
  subnet_id      = aws_subnet.public_subnet.id
  route_table_id = aws_route_table.public_rt.id
}

# Open Security Border Policies
resource "aws_security_group" "lab_sg" {
  name        = "platform-lab-security-perimeter"
  description = "Allows data plane traffic validation and terminal access ports"
  vpc_id      = aws_vpc.platform_vpc.id

  ingress {
    description = "Secure Shell Connection Channel"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "Proxy Data Plane Ingress"
    from_port   = 8080
    to_port     = 8080
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "Grafana Dashboard Interface Port"
    from_port   = 3000
    to_port     = 3000
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "Proxy Admin Controls & Telemetry Data Port"
    from_port   = 9090
    to_port     = 9090
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "platform-security-group" }
}

# Compute Node Laboratory Engine (Naked Setup)
resource "aws_instance" "platform_node" {
  ami                         = data.aws_ami.latest_ubuntu.id
  instance_type               = "t3.xlarge" 
  associate_public_ip_address = true 

  subnet_id              = aws_subnet.public_subnet.id
  vpc_security_group_ids = [aws_security_group.lab_sg.id]
  key_name               = "demo" 

  tags = { Name = "go-proxy-experimental-lab" }
}

output "public_ip" {
  value       = aws_instance.platform_node.public_ip
  description = "The dynamic routing address pointing to your active cloud workspace laboratory"
}