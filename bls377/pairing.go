// Copyright 2020 ConsenSys AG
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bls377

import (
	"errors"
	"sync"

	"github.com/consensys/gurvy/bls377/internal/fptower"
)

// GT target group of the pairing
type GT = fptower.E12

type lineEvaluation struct {
	r0 fptower.E2
	r1 fptower.E2
	r2 fptower.E2
}

// Pair calculates the reduced pairing for a set of points
func Pair(P []G1Affine, Q []G2Affine) (GT, error) {
	f, err := MillerLoop(P, Q)
	if err != nil {
		return GT{}, err
	}
	return FinalExponentiation(&f), nil
}

// PairingCheck calculates the reduced pairing for a set of points and returns True if the result is One
func PairingCheck(P []G1Affine, Q []G2Affine) (bool, error) {
	f, err := Pair(P, Q)
	if err != nil {
		return false, err
	}
	var one GT
	one.SetOne()
	return f.Equal(&one), nil
}

// FinalExponentiation computes the final expo x**(p**6-1)(p**2+1)(p**4 - p**2 +1)/r
func FinalExponentiation(z *GT, _z ...*GT) GT {

	var result GT
	result.Set(z)

	for _, e := range _z {
		result.Mul(&result, e)
	}

	// https://eprint.iacr.org/2016/130.pdf
	var t [6]GT

	// easy part
	t[0].Conjugate(&result)
	result.Inverse(&result)
	t[0].Mul(&t[0], &result)
	result.FrobeniusSquare(&t[0]).
		Mul(&result, &t[0])

	// hard part (up to permutation)
	t[0].InverseUnitary(&result).Square(&t[0])
	t[5].Expt(&result)
	t[1].CyclotomicSquare(&t[5])
	t[3].Mul(&t[0], &t[5])

	t[0].Expt(&t[3])
	t[2].Expt(&t[0])
	t[4].Expt(&t[2])

	t[4].Mul(&t[1], &t[4])
	t[1].Expt(&t[4])
	t[3].InverseUnitary(&t[3])
	t[1].Mul(&t[3], &t[1])
	t[1].Mul(&t[1], &result)

	t[0].Mul(&t[0], &result)
	t[0].FrobeniusCube(&t[0])

	t[3].InverseUnitary(&result)
	t[4].Mul(&t[3], &t[4])
	t[4].Frobenius(&t[4])

	t[5].Mul(&t[2], &t[5])
	t[5].FrobeniusSquare(&t[5])

	t[5].Mul(&t[5], &t[0])
	t[5].Mul(&t[5], &t[4])
	t[5].Mul(&t[5], &t[1])

	result.Set(&t[5])

	return result
}

var lineEvalPool = sync.Pool{
	New: func() interface{} {
		return new([69]lineEvaluation)
	},
}

// MillerLoop Miller loop
func MillerLoop(P []G1Affine, Q []G2Affine) (GT, error) {
	nP := len(P)
	if nP == 0 || nP != len(Q) {
		return GT{}, errors.New("invalid inputs sizes")
	}

	var (
		ch          = make([]chan struct{}, 0, nP)
		evaluations = make([]*[69]lineEvaluation, 0, nP)
	)

	var countInf = 0
	for k := 0; k < nP; k++ {
		if P[k].IsInfinity() || Q[k].IsInfinity() {
			countInf++
			continue
		}
		ch = append(ch, make(chan struct{}, 10))
		evaluations = append(evaluations, lineEvalPool.Get().(*[69]lineEvaluation))

		go preCompute(evaluations[k-countInf], &Q[k], &P[k], ch[k-countInf])
	}

	nP = nP - countInf

	var result GT
	result.SetOne()

	j := 0
	for i := len(loopCounter) - 2; i >= 0; i-- {

		result.Square(&result)
		for k := 0; k < nP; k++ {
			<-ch[k]
			mulAssign(&result, &evaluations[k][j])
		}
		j++

		if loopCounter[i] == 1 {
			for k := 0; k < nP; k++ {
				<-ch[k]
				mulAssign(&result, &evaluations[k][j])
			}
			j++
		}
	}

	// release objects into the pool
	for i := 0; i < len(evaluations); i++ {
		lineEvalPool.Put(evaluations[i])
	}

	return result, nil
}

// lineEval computes the evaluation of the line through Q, R (on the twist) at P
// Q, R are in jacobian coordinates
func lineEval(Q, R *G2Jac, P *G1Affine, result *lineEvaluation) {

	// converts _Q and _R to projective coords
	var _Q, _R g2Proj
	_Q.FromJacobian(Q)
	_R.FromJacobian(R)

	result.r1.Mul(&_Q.y, &_R.z)
	result.r0.Mul(&_Q.z, &_R.x)
	result.r2.Mul(&_Q.x, &_R.y)

	_Q.z.Mul(&_Q.z, &_R.y)
	_Q.x.Mul(&_Q.x, &_R.z)
	_Q.y.Mul(&_Q.y, &_R.x)

	result.r1.Sub(&result.r1, &_Q.z)
	result.r0.Sub(&result.r0, &_Q.x)
	result.r2.Sub(&result.r2, &_Q.y)

	result.r1.MulByElement(&result.r1, &P.X)
	result.r0.MulByElement(&result.r0, &P.Y)
}

func mulAssign(z *GT, l *lineEvaluation) *GT {

	var a, b, c GT
	a.MulByVW(z, &l.r1)
	b.MulByV(z, &l.r0)
	c.MulByV2W(z, &l.r2)
	z.Add(&a, &b).Add(z, &c)

	return z
}

// precomputes the line evaluations used during the Miller loop.
func preCompute(evaluations *[69]lineEvaluation, Q *G2Affine, P *G1Affine, ch chan struct{}) {

	var Q1, Q2, Qbuf G2Jac
	Q1.FromAffine(Q)
	Q2.FromAffine(Q)
	Qbuf.FromAffine(Q)

	j := 0

	for i := len(loopCounter) - 2; i >= 0; i-- {

		Q1.Set(&Q2)
		Q2.Double(&Q1).Neg(&Q2)
		lineEval(&Q1, &Q2, P, &evaluations[j]) // f(P), div(f) = 2(Q1)+(-2Q2)-3(O)
		ch <- struct{}{}
		Q2.Neg(&Q2)
		j++

		if loopCounter[i] == 1 {
			lineEval(&Q2, &Qbuf, P, &evaluations[j]) // f(P), div(f) = (Q2)+(Q)+(-Q2-Q)-3(O)
			ch <- struct{}{}
			Q2.AddMixed(Q)
			j++
		}
	}

	close(ch)
}
