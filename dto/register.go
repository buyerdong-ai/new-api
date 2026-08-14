package dto

type RegisterRequest struct {
	Method           string `json:"method" validate:"omitempty,oneof=email phone"`
	Username         string `json:"username" validate:"max=20"`
	Password         string `json:"password" validate:"min=8,max=20"`
	Email            string `json:"email" validate:"max=50"`
	Phone            string `json:"phone"`
	VerificationCode string `json:"verification_code"`
	AffCode          string `json:"aff_code"`
}