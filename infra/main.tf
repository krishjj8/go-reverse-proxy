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





resource "aws_iam_role" "proxy" {
  name = "go-proxy-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
    }]
  })
}
resource "aws_vpc" "proxy_vpc" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_hostnames = true
  tags = { Name = "Proxy-VPC" }
}

resource "aws_subnet" "proxy_public" {
  vpc_id                  = aws_vpc.proxy_vpc.id
  cidr_block              = "10.0.1.0/24"
  availability_zone       = "ap-south-1a"
  map_public_ip_on_launch = true
  tags = { Name = "Proxy-Public-Subnet" }
}

resource "aws_internet_gateway" "proxy_igw" {
  vpc_id = aws_vpc.proxy_vpc.id
  tags = { Name = "Proxy-IGW" }
}

resource "aws_route_table" "proxy_public" {
  vpc_id = aws_vpc.proxy_vpc.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.proxy_igw.id
  }
  tags = { Name = "Proxy-Public-RT" }
}

resource "aws_route_table_association" "proxy_public" {
  subnet_id      = aws_subnet.proxy_public.id
  route_table_id = aws_route_table.proxy_public.id
}

resource "aws_iam_role_policy_attachment" "cloudwatch" {
  role       = aws_iam_role.proxy.name
  policy_arn = "arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy"
}

resource "aws_iam_instance_profile" "proxy" {
  name = "go-proxy-profile"
  role = aws_iam_role.proxy.name
}

# Security group for the proxy instance
resource "aws_security_group" "proxy" {
  name        = "proxy-sg"
  description = "Go reverse proxy security group"
  vpc_id      = aws_vpc.proxy_vpc.id

  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    from_port   = 8080
    to_port     = 8080
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "proxy-sg" }
}

# EC2 instance
resource "aws_instance" "proxy" {
  ami                         = "ami-0f5ee92e2d63afc18"
  instance_type               = "t2.micro"
  subnet_id                   = aws_subnet.proxy_public.id
  vpc_security_group_ids      = [aws_security_group.proxy.id]
  key_name                    = "demo"
  iam_instance_profile        = aws_iam_instance_profile.proxy.name
  associate_public_ip_address = true

  user_data = <<-EOF
    #!/bin/bash
    apt update -y
    apt install -y curl

    # Create directory for proxy
    mkdir -p /opt/proxy

    # Create systemd service
    cat > /etc/systemd/system/go-proxy.service <<SERVICE
    [Unit]
    Description=Go Reverse Proxy
    After=network.target

    [Service]
    Type=simple
    User=ubuntu
    WorkingDirectory=/opt/proxy
    ExecStart=/opt/proxy/proxy
    Restart=always
    RestartSec=5

    [Install]
    WantedBy=multi-user.target
    SERVICE

    systemctl daemon-reload
    systemctl enable go-proxy
  EOF

  tags = { Name = "Go-Proxy-Server" }
}

output "proxy_public_ip" {
  value       = aws_instance.proxy.public_ip
  description = "Public IP of proxy EC2 instance"
}

output "proxy_instance_id" {
  value = aws_instance.proxy.id
}